import { useEffect, useRef, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";

type VersionSummary = {
  version: number;
  status: "draft" | "approved" | "published";
  createdAt: string;
  approvedAt?: string;
  publishedAt?: string;
};

type TemplateSummary = {
  slug: string;
  name: string;
  versions: VersionSummary[];
};

type TemplateVersion = VersionSummary & {
  slug: string;
  name: string;
  source: string;
  sampleData: Record<string, unknown>;
  dataSchema: Record<string, unknown>;
  schemaHash: string;
  storageKey: string;
};

type ContractDiagnostic = { severity: "error" | "warning"; message: string; offset?: number };
type ContractAnalysis = { sampleData: Record<string, unknown>; dataSchema: Record<string, unknown>; schemaHash: string; diagnostics: ContractDiagnostic[] };

type AuthUser = {
  subject: string;
  email?: string;
  name: string;
  admin: boolean;
};

const starterSource = `#set page(margin: 18mm)
#set text(size: 9pt)
#let report = json("report.json")

#align(center, text(size: 18pt, weight: "bold")[New Report])
#line(length: 100%, stroke: 1pt)
#v(12pt)

#text(size: 11pt, weight: "bold")[Sample Information]
#v(5pt)
#grid(
  columns: (32%, 1fr),
  [*Sample ID*], [#report.sample.id],
)
`;

const starterData = `{
  "sample": {
    "id": "26000123"
  }
}`;

function App() {
  const [templates, setTemplates] = useState<TemplateSummary[]>([]);
  const [selection, setSelection] = useState<{ slug: string; version: number } | null>(null);
  const [current, setCurrent] = useState<TemplateVersion | null>(null);
  const [source, setSource] = useState("");
  const [sampleData, setSampleData] = useState("{}");
  const [savedSource, setSavedSource] = useState("");
  const [savedSampleData, setSavedSampleData] = useState("{}");
  const [previewURL, setPreviewURL] = useState("");
  const [busy, setBusy] = useState("");
  const [message, setMessage] = useState("");
  const [dataOpen, setDataOpen] = useState(true);
  const [dataHeight, setDataHeight] = useState(253);
  const [newOpen, setNewOpen] = useState(false);
  const [activePane, setActivePane] = useState<"source" | "split" | "preview">(() =>
    window.matchMedia("(max-width: 1200px), (max-height: 760px)").matches ? "source" : "split",
  );
  const [splitSize, setSplitSize] = useState(50);
  const [previewExpanded, setPreviewExpanded] = useState(false);
  const [user, setUser] = useState<AuthUser | null>();
	const [contract, setContract] = useState<ContractAnalysis | null>(null);
	const [contractBusy, setContractBusy] = useState(false);
  const [area, setArea] = useState<"templates" | "reports" | "storage" | "clients" | "docs">("templates");
  const previewRequest = useRef<AbortController | null>(null);
	const contractRequest = useRef<AbortController | null>(null);

  async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const headers = new Headers(options.headers);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers });
    if (!response.ok) {
      if (response.status === 401) setUser(null);
      const body = await response.json().catch(() => null) as { error?: { message?: string } } | null;
      throw new Error(body?.error?.message ?? `${response.status} ${response.statusText}`);
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
  }

  async function refresh(preferred?: { slug: string; version: number }) {
    const items = await request<TemplateSummary[]>("/v1/templates");
    setTemplates(items);
    if (preferred) {
      setSelection(preferred);
      return;
    }
    if (!selection && items.length > 0 && items[0].versions.length > 0) {
      setSelection({ slug: items[0].slug, version: items[0].versions[0].version });
    }
  }

  useEffect(() => {
    fetch("/auth/me")
      .then(async (response) => {
        if (!response.ok) throw new Error("authentication required");
        setUser(await response.json() as AuthUser);
      })
      .catch(() => setUser(null));
  }, []);

  useEffect(() => {
    if (!user) return;
    refresh().catch((error: Error) => setMessage(error.message));
  }, [user?.subject]);

  useEffect(() => {
    if (!selection) return;
    previewRequest.current?.abort();
    previewRequest.current = null;
    setPreviewURL("");
    setPreviewExpanded(false);
    setBusy("loading");
    let active = true;
    request<TemplateVersion>(`/v1/templates/${selection.slug}/versions/${selection.version}`)
      .then((version) => {
        if (!active) return;
        setCurrent(version);
        setSource(version.source);
        setSavedSource(version.source);
        const formattedData = JSON.stringify(version.sampleData, null, 2);
        setSampleData(formattedData);
        setSavedSampleData(formattedData);
		setContract({ sampleData: version.sampleData, dataSchema: version.dataSchema, schemaHash: version.schemaHash, diagnostics: [] });
        setMessage("");
      })
      .catch((error: Error) => { if (active) setMessage(error.message); })
      .finally(() => { if (active) setBusy(""); });
    return () => { active = false; };
  }, [selection?.slug, selection?.version, user?.subject]);

  useEffect(() => () => {
    if (previewURL) URL.revokeObjectURL(previewURL);
  }, [previewURL]);

  function parsedData() {
    const parsed = JSON.parse(sampleData) as unknown;
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error("Test data must be a JSON object");
    return parsed;
  }

  async function save() {
    if (!current) return;
    setBusy("saving");
    try {
      const updated = await request<TemplateVersion>(`/v1/templates/${current.slug}/versions/${current.version}`, {
        method: "PUT",
        body: JSON.stringify({ source, sampleData: parsedData() }),
      });
      setCurrent(updated);
      const formattedData = JSON.stringify(updated.sampleData, null, 2);
      setSource(updated.source);
      setSampleData(formattedData);
      setSavedSource(updated.source);
      setSavedSampleData(formattedData);
		setContract({ sampleData: updated.sampleData, dataSchema: updated.dataSchema, schemaHash: updated.schemaHash, diagnostics: [] });
      setMessage("Draft saved");
      await refresh({ slug: updated.slug, version: updated.version });
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function preview(automatic = false) {
    let data: unknown;
    try {
      data = parsedData();
    } catch (error) {
      if (!automatic) setMessage((error as Error).message);
      return;
    }

    previewRequest.current?.abort();
    const controller = new AbortController();
    previewRequest.current = controller;
    setBusy(automatic ? "auto previewing" : "previewing");
    try {
      const headers = new Headers({ "Content-Type": "application/json" });
      const response = await fetch("/v1/templates/preview", {
        method: "POST",
        headers,
        signal: controller.signal,
        body: JSON.stringify({ source, sampleData: data }),
      });
      if (!response.ok) {
        const body = await response.json() as { error: { message: string } };
        throw new Error(body.error.message);
      }
      const blob = await response.blob();
      if (previewRequest.current !== controller) return;
      setPreviewURL(URL.createObjectURL(blob));
      setActivePane((pane) => pane === "split" ? "split" : "preview");
      setMessage(automatic ? "Preview updated" : "Preview compiled");
    } catch (error) {
      if ((error as Error).name === "AbortError") return;
      setMessage((error as Error).message);
    } finally {
      if (previewRequest.current === controller) {
        previewRequest.current = null;
        setBusy("");
      }
    }
  }

  async function transition(action: "approve" | "publish") {
    if (!current) return;
    setBusy(action);
    try {
      const updated = await request<TemplateVersion>(`/v1/templates/${current.slug}/versions/${current.version}/${action}`, { method: "POST" });
      setCurrent(updated);
      setMessage(action === "approve" ? "Version approved and locked" : "Version published");
      await refresh({ slug: updated.slug, version: updated.version });
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function createVersion() {
    if (!current) return;
    setBusy("creating");
    try {
      const created = await request<TemplateVersion>(`/v1/templates/${current.slug}/versions`, {
        method: "POST",
        body: JSON.stringify({ source, sampleData: parsedData() }),
      });
      await refresh({ slug: created.slug, version: created.version });
      setMessage(`Draft v${created.version} created`);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function discardDraft() {
    if (!current || current.status !== "draft" || !window.confirm(`Discard draft v${current.version}? This cannot be undone.`)) return;
    setBusy("discarding");
    try {
      await request<void>(`/v1/templates/${current.slug}/versions/${current.version}`, { method: "DELETE" });
      const template = templates.find((item) => item.slug === current.slug);
      const fallback = template?.versions.find((version) => version.status === "published");
      setCurrent(null);
      setSelection(fallback ? { slug: current.slug, version: fallback.version } : null);
      await refresh(fallback ? { slug: current.slug, version: fallback.version } : undefined);
      setMessage(`Draft v${current.version} discarded`);
    } catch (error) {
      setMessage((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function logout() {
    await fetch("/auth/logout", { method: "POST" });
    setUser(null);
  }

  const editable = !!user?.admin && current?.status === "draft";
  const dirty = editable && (source !== savedSource || sampleData !== savedSampleData);
  const selectedTemplate = templates.find((template) => template.slug === current?.slug);
  const candidate = selectedTemplate?.versions.find((version) => version.status === "draft" || version.status === "approved");
  const contractDiagnostics = contract?.diagnostics ?? [];
  const contractBlocked = contractDiagnostics.some((diagnostic) => diagnostic.severity === "error");

  function selectVersion(next: { slug: string; version: number }) {
    if (dirty && !window.confirm("Discard unsaved changes and open another version?")) return;
    setSelection(next);
  }

  function openOrCreateDraft() {
    if (current && candidate && candidate.version !== current.version) {
      selectVersion({ slug: current.slug, version: candidate.version });
      return;
    }
    createVersion();
  }

  function startSplitResize(event: React.PointerEvent<HTMLDivElement>) {
    event.preventDefault();
    const grid = event.currentTarget.parentElement;
    if (!grid) return;
    const compact = window.matchMedia("(max-width: 1200px), (max-height: 760px)").matches;
    const bounds = grid.getBoundingClientRect();
    const cursor = compact ? "row-resize" : "col-resize";
    document.body.style.cursor = cursor;
    document.body.style.userSelect = "none";

    function resize(moveEvent: PointerEvent) {
      const position = compact ? moveEvent.clientY - bounds.top : moveEvent.clientX - bounds.left;
      const total = compact ? bounds.height : bounds.width;
      setSplitSize(Math.min(80, Math.max(20, (position / total) * 100)));
    }
    function stop() {
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      window.removeEventListener("pointermove", resize);
      window.removeEventListener("pointerup", stop);
      window.removeEventListener("pointercancel", stop);
    }
    window.addEventListener("pointermove", resize);
    window.addEventListener("pointerup", stop);
    window.addEventListener("pointercancel", stop);
  }

  function startDataResize(event: React.PointerEvent<HTMLDivElement>) {
    event.preventDefault();
    const startY = event.clientY;
    const startHeight = dataHeight;
    document.body.style.cursor = "row-resize";
    document.body.style.userSelect = "none";

    function resize(moveEvent: PointerEvent) {
      const maximum = Math.max(180, window.innerHeight * 0.65);
      setDataHeight(Math.min(maximum, Math.max(120, startHeight - (moveEvent.clientY - startY))));
    }
    function stop() {
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      window.removeEventListener("pointermove", resize);
      window.removeEventListener("pointerup", stop);
      window.removeEventListener("pointercancel", stop);
    }
    window.addEventListener("pointermove", resize);
    window.addEventListener("pointerup", stop);
    window.addEventListener("pointercancel", stop);
  }

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (!(event.metaKey || event.ctrlKey)) return;
      if (event.key.toLowerCase() === "s" && editable) {
        event.preventDefault();
        save();
      }
      if (event.key === "Enter" && current) {
        event.preventDefault();
        preview();
      }
    }
    function beforeUnload(event: BeforeUnloadEvent) {
      if (dirty) event.preventDefault();
    }
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("beforeunload", beforeUnload);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("beforeunload", beforeUnload);
    };
  }, [editable, dirty, current, source, sampleData]);

  useEffect(() => {
    if (!previewExpanded) return;
    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") setPreviewExpanded(false);
    }
    window.addEventListener("keydown", closeOnEscape);
    return () => window.removeEventListener("keydown", closeOnEscape);
  }, [previewExpanded]);

  useEffect(() => {
    if (!current || !selection || current.slug !== selection.slug || current.version !== selection.version) return;
    try {
      parsedData();
    } catch {
      return;
    }
    const timeout = window.setTimeout(() => preview(true), 1600);
    return () => window.clearTimeout(timeout);
  }, [current?.slug, current?.version, selection?.slug, selection?.version, source, sampleData]);

	useEffect(() => {
		if (!editable || !current || !selection || current.slug !== selection.slug || current.version !== selection.version) return;
		let data: unknown;
		try { data = parsedData(); } catch { return; }
		contractRequest.current?.abort();
		const controller = new AbortController();
		contractRequest.current = controller;
		setContractBusy(true);
		const timeout = window.setTimeout(() => {
			request<ContractAnalysis>("/v1/templates/analyze", {
				method: "POST", signal: controller.signal, body: JSON.stringify({ source, sampleData: data }),
			}).then((analysis) => {
				if (contractRequest.current !== controller) return;
				setContract({ ...analysis, diagnostics: analysis.diagnostics ?? [] });
				const merged = JSON.stringify(analysis.sampleData, null, 2);
				if (merged !== sampleData) setSampleData(merged);
			}).catch((error: Error) => {
				if (error.name !== "AbortError" && contractRequest.current === controller) setMessage(error.message);
			}).finally(() => {
				if (contractRequest.current === controller) { contractRequest.current = null; setContractBusy(false); }
			});
		}, 550);
		return () => {
			window.clearTimeout(timeout);
			controller.abort();
			if (contractRequest.current === controller) { contractRequest.current = null; setContractBusy(false); }
		};
	}, [editable, current?.slug, current?.version, selection?.slug, selection?.version, source, sampleData]);

  if (user === null) {
    return <main className="login-shell">
      <section className="login-card">
        <div className="brand-mark">T</div>
        <div className="eyebrow">Typst Reports</div>
        <h1>Report template workbench</h1>
        <p>Sign in through Authentik to review templates, compile previews, and manage published report versions.</p>
        <a className="login-button" href="/auth/login">Sign in with Authentik</a>
      </section>
    </main>;
  }

  if (user === undefined) {
    return <main className="login-shell"><div className="loading-mark">T</div></main>;
  }

  return (
    <main className="app-shell">
      <header className="topbar">
        <div className="brand-mark">T</div>
        <div className="brand-copy"><strong>Typst Reports</strong><span>Template workbench</span></div>
        <div className="topbar-actions">
          <nav className="area-nav" aria-label="Application workspaces">
            <button className={area === "templates" ? "active" : ""} onClick={() => setArea("templates")}>Templates</button>
            {user.admin && <button className={area === "reports" ? "active" : ""} onClick={() => setArea("reports")}>Reports</button>}
            {user.admin && <button className={area === "storage" ? "active" : ""} onClick={() => setArea("storage")}>Storage</button>}
            {user.admin && <button className={area === "clients" ? "active" : ""} onClick={() => setArea("clients")}>API Clients</button>}
            <button className={area === "docs" ? "active" : ""} onClick={() => setArea("docs")}>Docs</button>
          </nav>
          <span className={`connection ${message.toLowerCase().includes("failed") ? "bad" : ""}`}>{busy || message || "Ready"}</span>
          <span className="identity"><strong>{user.name}</strong><small>{user.admin ? "Administrator" : "Viewer"}</small></span>
          <button className="icon-button" onClick={logout}>Sign out</button>
        </div>
      </header>

      {area === "templates" ? <><aside className="sidebar">
        <div className="sidebar-heading"><span>Templates</span>{user.admin && <button onClick={() => setNewOpen(true)}>+</button>}</div>
        <div className="template-list">
          {templates.map((template) => (
            <section className="template-group" key={template.slug}>
              <div className="template-name">{template.name}</div>
              <div className="template-slug">{template.slug}</div>
              <div className="version-list">
                {template.versions.map((version) => (
                  <button
                    className={selection?.slug === template.slug && selection.version === version.version ? "version active" : "version"}
                    key={version.version}
                    onClick={() => selectVersion({ slug: template.slug, version: version.version })}
                  >
                    <span>v{version.version}</span><Status status={version.status} />
                  </button>
                ))}
              </div>
            </section>
          ))}
        </div>
      </aside>

      <section className="workspace">
        <div className="document-bar">
          <div>
            <div className="eyebrow">{current?.slug ?? "No template selected"}</div>
            <h1>{current?.name ?? "Template Manager"} {current && <span>v{current.version}</span>}</h1>
          </div>
          <div className="document-actions">
            {user.admin && current?.status === "published" && <button className="primary" onClick={openOrCreateDraft} disabled={!!busy}>{candidate ? `Open ${candidate.status} v${candidate.version}` : "Create editable draft"}</button>}
            <button className="secondary" onClick={() => preview()} disabled={!current || !!busy}>Compile preview</button>
            {editable && <button className="secondary danger" onClick={discardDraft} disabled={!!busy}>Discard</button>}
            {editable && <button className="primary" onClick={save} disabled={!!busy || !dirty}>Save draft</button>}
            {editable && <button className="approve" onClick={() => transition("approve")} disabled={!!busy || dirty || contractBusy || contractBlocked}>Approve</button>}
            {user.admin && current?.status === "approved" && <button className="publish" onClick={() => transition("publish")} disabled={!!busy}>Publish</button>}
          </div>
        </div>

        <div className={`workflow-strip ${current?.status ?? "empty"}`}>
          {current?.status === "draft" && <><strong>Editing draft v{current.version}</strong><span>{contractBusy ? "Analyzing contract..." : contractBlocked ? contractDiagnostics.map((diagnostic) => diagnostic.message).join("; ") : dirty ? "Unsaved changes" : "All changes saved"}</span></>}
          {current?.status === "approved" && <><strong>Approved v{current.version}</strong><span>This version is locked and ready to publish.</span></>}
          {current?.status === "published" && <><strong>Published versions are immutable</strong><span>{user.admin ? (candidate ? `Continue with ${candidate.status} v${candidate.version} to make changes.` : "Create a draft to begin editing.") : "You have read-only access."}</span></>}
          {!current && <><strong>Select a template</strong><span>Choose a version or create a new report template.</span></>}
          <div className="view-switcher" aria-label="Workspace view">
			{contract?.schemaHash && <code className="contract-hash" title={contract.schemaHash}>contract {contract.schemaHash.slice(0, 12)}</code>}
            <button className={activePane === "source" ? "active" : ""} onClick={() => setActivePane("source")}>Source</button>
            <button className={activePane === "split" ? "active" : ""} onClick={() => setActivePane("split")}>Split</button>
            <button className={activePane === "preview" ? "active" : ""} onClick={() => setActivePane("preview")}>Preview</button>
          </div>
        </div>

        <div className={`work-grid show-${activePane}`} style={{ "--split-size": `${splitSize}%` } as React.CSSProperties}>
          <section className="editor-pane">
            <div className="pane-label"><span>Typst source</span><span>{editable ? (dirty ? "Modified" : "Editable draft") : "Read only"}</span></div>
            <CodeMirror value={source} onChange={setSource} editable={editable} height="100%" theme="dark" basicSetup={{ lineNumbers: true, foldGutter: false }} />
          </section>
          <div
            className="split-handle"
            role="separator"
            aria-label="Resize source and preview panels"
            aria-valuemin={20}
            aria-valuemax={80}
            aria-valuenow={Math.round(splitSize)}
            tabIndex={activePane === "split" ? 0 : -1}
            onPointerDown={startSplitResize}
            onKeyDown={(event) => {
              if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
                event.preventDefault();
                setSplitSize((size) => Math.max(20, size - 5));
              } else if (event.key === "ArrowRight" || event.key === "ArrowDown") {
                event.preventDefault();
                setSplitSize((size) => Math.min(80, size + 5));
              }
            }}
          />
          <section className="preview-pane">
            <div className="pane-label"><span>PDF preview</span><div className="pane-meta"><span>{previewURL ? "Latest compile" : "Not compiled"}</span><button onClick={() => setPreviewExpanded(true)} disabled={!previewURL}>Expand</button></div></div>
            <div className="paper-stage">
              {previewURL ? <iframe src={previewURL} title="Compiled PDF preview" /> : <div className="empty-preview"><b>A4</b><p>Compile the template to inspect its PDF output.</p></div>}
            </div>
          </section>
        </div>

        <section className={dataOpen ? "data-drawer open" : "data-drawer"} style={{ "--data-height": `${dataHeight}px` } as React.CSSProperties}>
          {dataOpen && <div
            className="data-resize-handle"
            role="separator"
            aria-label="Resize test data panel"
            aria-valuemin={120}
            aria-valuemax={Math.round(window.innerHeight * 0.65)}
            aria-valuenow={Math.round(dataHeight)}
            tabIndex={0}
            onPointerDown={startDataResize}
            onKeyDown={(event) => {
              if (event.key === "ArrowUp") {
                event.preventDefault();
                setDataHeight((height) => Math.min(window.innerHeight * 0.65, height + 20));
              } else if (event.key === "ArrowDown") {
                event.preventDefault();
                setDataHeight((height) => Math.max(120, height - 20));
              }
            }}
          />}
          <button className="drawer-toggle" onClick={() => setDataOpen(!dataOpen)}><span>Test data <small>report.json / data.json</small></span><span>{dataOpen ? "Hide" : "Show"}</span></button>
          {dataOpen && <div className="data-editor"><CodeMirror className="data-code-editor" value={sampleData} onChange={setSampleData} editable={editable} height="100%" theme="dark" basicSetup={{ lineNumbers: true, foldGutter: true }} /></div>}
        </section>
      </section></> : area === "reports" ? <ReportsArea /> : area === "storage" ? <StorageArea /> : area === "clients" ? <APIClientsArea /> : <DocsArea />}

      {newOpen && <NewTemplate onClose={() => setNewOpen(false)} onCreated={async (created) => {
        setNewOpen(false);
        await refresh({ slug: created.slug, version: created.version });
      }} />}

      {previewExpanded && previewURL && <div className="preview-overlay" role="dialog" aria-modal="true" aria-label="Expanded PDF preview" onClick={() => setPreviewExpanded(false)}>
        <section className="preview-dialog" onClick={(event) => event.stopPropagation()}>
          <header><div><span>PDF preview</span><strong>{current?.name ?? "Report"} {current && `v${current.version}`}</strong></div><button onClick={() => setPreviewExpanded(false)}>Close</button></header>
          <iframe src={previewURL} title="Expanded compiled PDF preview" />
        </section>
      </div>}
    </main>
  );
}

function DocsArea() {
  const origin = window.location.origin;
  return <section className="docs-workspace">
    <aside className="docs-index">
      <div><span>Reference</span><strong>Service handbook</strong></div>
      <nav aria-label="Documentation sections">
        <a href="#docs-overview">Overview</a>
        <a href="#docs-auth">Authentication</a>
        <a href="#docs-contracts">Contracts</a>
        <a href="#docs-submit">Submit reports</a>
        <a href="#docs-poll">Poll and search</a>
        <a href="#docs-download">Download PDFs</a>
        <a href="#docs-errors">Errors and limits</a>
        <a href="#docs-templates">Template lifecycle</a>
        <a href="#docs-operations">Operations</a>
        <a href="#docs-storage">Storage</a>
        <a href="#docs-authentik">Authentik</a>
        <a href="#docs-config">Configuration</a>
      </nav>
      <p>Examples use the current service origin and a managed API key.</p>
    </aside>

    <article className="docs-content">
      <header className="docs-hero" id="docs-overview">
        <div className="eyebrow">Developer and operator reference</div>
        <h1>Build against immutable report contracts.</h1>
        <p>The service turns validated JSON into version-pinned Typst reports. Application clients discover a contract, submit an asynchronous job, poll its state, and download an integrity-checked PDF.</p>
        <div className="docs-flow" aria-label="Integration workflow">
          <span><b>01</b> Discover</span><i />
          <span><b>02</b> Validate</span><i />
          <span><b>03</b> Submit</span><i />
          <span><b>04</b> Poll</span><i />
          <span><b>05</b> Download</span>
        </div>
      </header>

      <DocSection id="docs-auth" label="Machine access" title="Authentication and scopes">
        <p>An administrator creates a client and issues a key in <strong>API Clients</strong>. The plaintext key is shown once. Send it as a bearer token; <code>X-API-Key</code> remains available for compatibility.</p>
        <CodeSample language="Example environment">{`export REPORT_API_URL='${origin}'
export REPORT_API_KEY='rpt_live_...'`}</CodeSample>
        <CodeSample>{`Authorization: Bearer rpt_live_<key-id>_<secret>`}</CodeSample>
        <div className="docs-callout warning"><strong>Trust boundary</strong><span>Scopes grant service-wide access. Jobs are not isolated by API client, so issue keys only to trusted applications and grant the minimum scopes.</span></div>
        <div className="docs-table-wrap"><table><thead><tr><th>Scope</th><th>Access</th></tr></thead><tbody>
          <tr><td><code>templates:read</code></td><td>Published versions and schemas</td></tr>
          <tr><td><code>reports:submit</code></td><td>Create asynchronous report jobs</td></tr>
          <tr><td><code>reports:read</code></td><td>Poll and search jobs</td></tr>
          <tr><td><code>reports:download</code></td><td>Download completed PDFs</td></tr>
        </tbody></table></div>
      </DocSection>

      <DocSection id="docs-contracts" label="Step 1" title="Discover published contracts">
        <Endpoint method="GET" path="/v1/report-templates" scope="templates:read" />
        <p>The catalog contains only published, immutable versions. Select a numeric version and retain its <code>schemaHash</code>. Validate outgoing data against the returned draft 2020-12 <code>dataSchema</code>.</p>
        <CodeSample>{`curl --fail-with-body \\
  -H "Authorization: Bearer $REPORT_API_KEY" \\
  "$REPORT_API_URL/v1/report-templates"`}</CodeSample>
        <CodeSample language="Response - 200 OK">{`{
  "items": [{
    "slug": "certificate-of-analysis",
    "name": "Certificate of Analysis",
    "latestVersion": 2,
    "versions": [{
      "version": 2,
      "publishedAt": "2026-09-23T21:54:00Z",
      "dataSchema": {"$schema": "https://json-schema.org/draft/2020-12/schema", "type": "object"},
      "schemaHash": "<schema-sha256>"
    }]
  }]
}`}</CodeSample>
        <p className="docs-note">Versions published before contract analysis may expose a permissive object schema for compatibility. The catalog remains authoritative for every version.</p>
      </DocSection>

      <DocSection id="docs-submit" label="Steps 2-3" title="Validate and submit">
        <Endpoint method="POST" path="/v1/reports" scope="reports:submit" />
        <p>Pin both <code>version</code> and <code>schemaHash</code> in controlled integrations. Omitting <code>version</code> selects the latest published version atomically, but a new publication can make an existing payload invalid.</p>
        <CodeSample>{`curl --fail-with-body -X POST "$REPORT_API_URL/v1/reports" \\
  -H "Authorization: Bearer $REPORT_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "template": "certificate-of-analysis",
    "version": 2,
    "schemaHash": "<catalog-schema-hash>",
    "data": {
      "sample": {"id": "26000123", "description": "Example sample"},
      "results": [{"test": "APC", "result": "<10"}]
    }
  }'`}</CodeSample>
        <div className="docs-table-wrap"><table><thead><tr><th>Field</th><th>Requirement</th><th>Meaning</th></tr></thead><tbody>
          <tr><td><code>template</code></td><td>Required</td><td>Published slug from the catalog</td></tr>
          <tr><td><code>version</code></td><td>Recommended</td><td>Positive published version; omit only to select latest</td></tr>
          <tr><td><code>schemaHash</code></td><td>Recommended</td><td>Exact hash from the selected catalog version</td></tr>
          <tr><td><code>data</code></td><td>Required</td><td>JSON satisfying the selected schema; maximum 2 MiB</td></tr>
        </tbody></table></div>
        <CodeSample language="Response - 202 Accepted">{`{
  "jobId": "01M384CQDRYVREG5FZN72VQWX3",
  "status": "queued",
  "template": "certificate-of-analysis",
  "templateVersion": 2,
  "schemaHash": "<resolved-schema-sha256>"
}`}</CodeSample>
        <div className="docs-callout"><strong>Durable submission</strong><span>Contract resolution, validation, job creation, and queue intent creation share one PostgreSQL transaction. Redis downtime does not require resubmission.</span></div>
        <div className="docs-callout warning"><strong>No submission idempotency</strong><span>Every successful POST creates a new job. Persist the returned job ID promptly; retrying after an ambiguous network failure can create a duplicate.</span></div>
      </DocSection>

      <DocSection id="docs-poll" label="Step 4" title="Poll and search jobs">
        <Endpoint method="GET" path="/v1/reports/{jobId}" scope="reports:read" />
        <p>Poll with bounded exponential backoff. A temporary failure may return a processing job to <code>queued</code>, so progress is not strictly monotonic.</p>
        <div className="state-track">
          <span className="queued">queued</span><b>-&gt;</b><span className="processing">processing</span><b>-&gt;</b><span className="completed">completed</span>
          <small>processing may return to queued for an automatic retry, or finish as failed</small>
        </div>
        <CodeSample language="Completed job - 200 OK">{`{
  "jobId": "01M384CQDRYVREG5FZN72VQWX3",
  "template": "certificate-of-analysis",
  "templateVersion": 2,
  "status": "completed",
  "dataSha256": "<input-sha256>",
  "createdAt": "2026-09-23T21:57:47.576758Z",
  "completedAt": "2026-09-23T21:57:49.283798Z",
  "attempts": 1,
  "sha256": "<pdf-sha256>",
  "rendererVersion": "typst-0.13.1",
  "downloadUrl": "/v1/reports/01M384CQDRYVREG5FZN72VQWX3/download",
  "schemaHash": "<resolved-schema-sha256>",
  "retryChildren": []
}`}</CodeSample>
        <Endpoint method="GET" path="/v1/reports" scope="reports:read" />
        <div className="docs-table-wrap"><table><thead><tr><th>Query</th><th>Behavior</th></tr></thead><tbody>
          <tr><td><code>status</code></td><td>Exact queued, processing, completed, or failed</td></tr>
          <tr><td><code>template</code> / <code>templateVersion</code></td><td>Exact template slug and positive version</td></tr>
          <tr><td><code>sampleId</code></td><td>Case-insensitive text search across serialized input</td></tr>
          <tr><td><code>requestedBy</code></td><td>Exact verified caller identity</td></tr>
          <tr><td><code>createdFrom</code> / <code>createdTo</code></td><td>Inclusive/exclusive RFC3339 range</td></tr>
          <tr><td><code>limit</code> / <code>offset</code></td><td>Offset pagination; limit defaults to 50 and cannot exceed 200</td></tr>
        </tbody></table></div>
      </DocSection>

      <DocSection id="docs-download" label="Step 5" title="Download and verify the PDF">
        <Endpoint method="GET" path="/v1/reports/{jobId}/download" scope="reports:download" />
        <p>The API reads through managed storage and verifies the recorded SHA-256 before serving a completed PDF. <code>HEAD</code> returns the same metadata without a body.</p>
        <CodeSample>{`curl --fail-with-body \\
  -H "Authorization: Bearer $REPORT_API_KEY" \\
  -o report.pdf \\
  "$REPORT_API_URL/v1/reports/$JOB_ID/download"`}</CodeSample>
        <CodeSample language="Response headers - 200 OK">{`Content-Type: application/pdf
Content-Disposition: attachment; filename="certificate-of-analysis-v2-<jobId>.pdf"
Cache-Control: private, no-store
ETag: "<pdf-sha256>"
X-Content-SHA256: <pdf-sha256>`}</CodeSample>
      </DocSection>

      <DocSection id="docs-errors" label="Protocol" title="Errors, retries, and limits">
        <CodeSample language="Error envelope">{`{
  "error": {
    "code": "data_validation_failed",
    "message": "report data failed template contract validation",
    "details": [{"path": "$.sample.id", "message": "required field is missing"}]
  }
}`}</CodeSample>
        <p>Branch on the HTTP status and <code>error.code</code>, not message text. Retry network errors and <code>5xx</code> responses with backoff; correct the request or credentials before retrying other responses.</p>
        <div className="docs-table-wrap"><table><thead><tr><th>Status</th><th>Important codes</th><th>Meaning</th></tr></thead><tbody>
          <tr><td>400</td><td><code>invalid_request</code>, <code>invalid_*</code></td><td>Malformed body or filter</td></tr>
          <tr><td>401 / 403</td><td><code>invalid_api_key</code>, <code>insufficient_scope</code></td><td>Authentication or scope failure</td></tr>
          <tr><td>404</td><td><code>not_found</code></td><td>Unknown job or endpoint</td></tr>
          <tr><td>409</td><td><code>template_contract_changed</code>, <code>report_not_ready</code></td><td>Refresh the contract or continue polling</td></tr>
          <tr><td>413</td><td><code>request_too_large</code></td><td>Report data exceeds 2 MiB</td></tr>
          <tr><td>422</td><td><code>template_not_found</code>, <code>data_validation_failed</code></td><td>Unavailable version or contract violation</td></tr>
          <tr><td>500 / 503</td><td><code>internal_error</code>, <code>storage_unavailable</code></td><td>Transient server or storage failure</td></tr>
        </tbody></table></div>
        <div className="docs-metrics"><div><strong>10 MiB</strong><span>maximum JSON body</span></div><div><strong>2 MiB</strong><span>maximum report data</span></div><div><strong>200</strong><span>maximum list page</span></div><div><strong>60s</strong><span>default render timeout</span></div></div>
      </DocSection>

      <DocSection id="docs-templates" label="Authoring" title="Template lifecycle and contracts">
        <div className="lifecycle"><span>draft</span><b>-&gt;</b><span>approved</span><b>-&gt;</b><span>published</span></div>
        <p>Drafts contain editable Typst source and sample JSON. Contract analysis infers explicit data access, merges missing sample fields, and produces a deterministic schema hash. Approval validates the sample and compiles the template. Approved versions are locked; publication stores the immutable artifact for workers.</p>
        <p>Templates load report input from either <code>json("report.json")</code> or <code>json("data.json")</code>. Analysis supports dotted paths, aliases, loop aliases, and literal <code>.at("field", default: ...)</code>. Dynamic rooted lookup and unsupported metaprogramming must be rewritten into explicit access before approval.</p>
      </DocSection>

      <DocSection id="docs-operations" label="Administrator" title="Report operations">
        <p>The <strong>Reports</strong> workspace exposes queue and worker health, searchable immutable metadata, attempt timelines, downloads, retry lineage, and guarded stale recovery.</p>
        <div className="docs-columns"><div><strong>Retry failed work</strong><p>Retry creates a new linked job with the original data and template version. It never overwrites the failed job or an existing PDF.</p></div><div><strong>Recover stale work</strong><p>Recovery is allowed only after the processing lease expires and the owning worker heartbeat is stale. The action returns the same job to the durable queue.</p></div></div>
        <p className="docs-note">Workers heartbeat every 12 seconds. Attempt and recovery events are append-only. The operations API is browser administrator-only and derives its audit identity from OIDC.</p>
      </DocSection>

      <DocSection id="docs-storage" label="Administrator" title="Storage profiles and migration">
        <p>The deployment-managed filesystem profile is always available. Administrators can add private AWS S3, Cloudflare R2, MinIO, or other S3-compatible profiles; credentials are encrypted in PostgreSQL and never returned by the API.</p>
        <ol className="docs-steps">
          <li><b>Test</b><span>Create a profile only after its write/read/delete connectivity probe succeeds.</span></li>
          <li><b>Copy</b><span>Start a migration that snapshots known immutable objects and verifies digest and size at the destination.</span></li>
          <li><b>Observe</b><span>Pause, resume, or cancel dispatch while normal reads continue through available placements.</span></li>
          <li><b>Cut over</b><span>Explicitly set the destination as default after reviewing completion. Copying never changes the default automatically.</span></li>
        </ol>
        <div className="docs-callout warning"><strong>Required secret</strong><span><code>STORAGE_MASTER_KEY</code> must be the same base64-encoded 32-byte key on every API and worker replica. Losing or changing it makes managed credentials unreadable.</span></div>
      </DocSection>

      <DocSection id="docs-authentik" label="Production access" title="Authentik OIDC">
        <p>Browser access uses the authorization-code flow with PKCE. Authenticated users can view templates and compile previews; members of <code>OIDC_ADMIN_GROUP</code> can manage templates, reports, storage, and API clients.</p>
        <ol className="docs-steps">
          <li><b>Provider</b><span>Create an OAuth2/OpenID provider with the authorization-code flow.</span></li>
          <li><b>Redirect</b><span>Allow the exact public URL <code>https://reports.example.com/auth/callback</code>.</span></li>
          <li><b>Claims</b><span>Include <code>openid</code>, <code>profile</code>, <code>email</code>, and a <code>groups</code> claim.</span></li>
          <li><b>Group</b><span>Create <code>report-admins</code>, or configure another group name.</span></li>
        </ol>
      </DocSection>

      <DocSection id="docs-config" label="Deployment reference" title="Runtime configuration">
        <div className="docs-table-wrap"><table><thead><tr><th>Variable</th><th>Purpose</th></tr></thead><tbody>
          <tr><td><code>APP_ROLE</code></td><td><code>api</code> or <code>worker</code></td></tr>
          <tr><td><code>APP_ENV</code></td><td>Use <code>production</code> to enforce production secrets and OIDC</td></tr>
          <tr><td><code>DATABASE_URL</code></td><td>PostgreSQL connection string</td></tr>
          <tr><td><code>REDIS_ADDRESS</code></td><td>Redis queue address</td></tr>
          <tr><td><code>OIDC_ISSUER_URL</code>, <code>OIDC_CLIENT_ID</code>, <code>OIDC_CLIENT_SECRET</code></td><td>Authentik provider configuration</td></tr>
          <tr><td><code>OIDC_REDIRECT_URL</code></td><td>Public <code>/auth/callback</code> URL</td></tr>
          <tr><td><code>OIDC_ADMIN_GROUP</code></td><td>Group granted administrative access; defaults to <code>report-admins</code></td></tr>
          <tr><td><code>SESSION_SECRET</code></td><td>At least 32 characters for signed browser sessions</td></tr>
          <tr><td><code>STORAGE_MASTER_KEY</code></td><td>Base64 encoding of exactly 32 bytes, shared by every replica</td></tr>
          <tr><td><code>REPORTS_DIRECTORY</code></td><td>Persistent filesystem artifact root</td></tr>
          <tr><td><code>WORKER_CONCURRENCY</code></td><td>Concurrent report render jobs</td></tr>
          <tr><td><code>RENDER_TIMEOUT</code></td><td>Per-report Typst timeout; defaults to 60 seconds</td></tr>
          <tr><td><code>RENDERER_VERSION</code></td><td>Audit version recorded on report jobs</td></tr>
        </tbody></table></div>
        <p className="docs-note"><code>GET /healthz</code> is unauthenticated and checks PostgreSQL and Redis readiness. Point the public domain only at the API service on container port 8080; workers and data services remain private.</p>
      </DocSection>
    </article>
  </section>;
}

function DocSection({ id, label, title, children }: { id: string; label: string; title: string; children: React.ReactNode }) {
  return <section className="doc-section" id={id}><header><span>{label}</span><h2>{title}</h2></header><div className="doc-body">{children}</div></section>;
}

function Endpoint({ method, path, scope }: { method: string; path: string; scope: string }) {
  return <div className="endpoint"><strong>{method}</strong><code>{path}</code><span>{scope}</span></div>;
}

function CodeSample({ language = "Request", children }: { language?: string; children: string }) {
  return <div className="code-sample"><div>{language}</div><pre><code>{children}</code></pre></div>;
}

function Status({ status }: { status: VersionSummary["status"] }) {
  return <span className={`status ${status}`}>{status}</span>;
}

type ReportJob = {
  jobId: string; template: string; templateVersion: number; status: "queued" | "processing" | "completed" | "failed";
  dataSha256: string; requestedBy?: string; createdAt: string; startedAt?: string; completedAt?: string; attempts: number;
  error?: string; storageProfile?: string; sha256?: string; pages?: number; rendererVersion?: string; downloadUrl?: string;
  schemaHash: string; retryParentId?: string; retryChildren: string[]; processingWorkerId?: string; processingLeaseUntil?: string;
};
type AttemptEvent = { id: number; attempt: number; type: string; workerId?: string; actor?: string; message?: string; occurredAt: string };
type ReportDetail = ReportJob & { attemptTimeline: AttemptEvent[] };
type ReportsResult = { items: ReportJob[]; limit: number; offset: number; hasMore: boolean };
type OperationsHealth = {
  database: {
    workers: { workerId: string; lastSeenAt: string; ageSeconds: number; online: boolean; concurrency: number; rendererVersion: string; currentJobs: number }[];
    reportCounts: Record<string, number>; oldestQueuedAgeSeconds?: number; outboxPending: number; migrationsPending: number; migrationsRunning: number;
  };
  queues: { queue: string; pending: number; active: number; scheduled: number; retry: number; archived: number; latencyMs: number; paused: boolean }[];
  queueError?: string;
};

function ReportsArea() {
  const pageSize = 25;
  const [filters, setFilters] = useState({ sampleId: "", template: "", templateVersion: "", status: "", requestedBy: "", createdFrom: "", createdTo: "" });
  const [applied, setApplied] = useState(filters);
  const [offset, setOffset] = useState(0);
  const [result, setResult] = useState<ReportsResult>({ items: [], limit: pageSize, offset: 0, hasMore: false });
  const [selected, setSelected] = useState("");
  const [detail, setDetail] = useState<ReportDetail | null>(null);
  const [health, setHealth] = useState<OperationsHealth | null>(null);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  async function call<T>(path: string, options: RequestInit = {}) {
    const response = await fetch(path, options);
    const body = await response.json().catch(() => null);
    if (!response.ok) throw new Error(body?.error?.message ?? "Request failed");
    return body as T;
  }
  function queryString(nextOffset = offset) {
    const query = new URLSearchParams({ limit: String(pageSize), offset: String(nextOffset) });
    Object.entries(applied).forEach(([key, value]) => {
      if (!value) return;
      if (key === "createdFrom" || key === "createdTo") query.set(key, new Date(value).toISOString());
      else query.set(key, value);
    });
    return query.toString();
  }
  async function refreshReports(nextOffset = offset) {
    const next = await call<ReportsResult>(`/v1/reports?${queryString(nextOffset)}`);
    setResult(next);
    setOffset(nextOffset);
    setSelected((current) => next.items.some((item) => item.jobId === current) ? current : next.items[0]?.jobId || "");
  }
  async function refreshHealth() { setHealth(await call<OperationsHealth>("/v1/admin/operations/health")); }
  async function refreshDetail(id = selected) {
    if (!id) { setDetail(null); return; }
    setDetail(await call<ReportDetail>(`/v1/admin/reports/${id}`));
  }
  useEffect(() => { refreshReports().catch((error: Error) => setMessage(error.message)); }, [applied]);
  useEffect(() => { refreshDetail().catch((error: Error) => setMessage(error.message)); }, [selected]);
  useEffect(() => {
    refreshHealth().catch((error: Error) => setMessage(error.message));
    const timer = window.setInterval(() => refreshHealth().catch(() => {}), 12000);
    return () => window.clearInterval(timer);
  }, []);
  async function operate(action: "retry" | "recover") {
    if (!detail) return;
    const prompt = action === "retry" ? "Create a new report from this immutable failed input?" : "Recover this job only if its lease and worker heartbeat are stale?";
    if (!window.confirm(prompt)) return;
    setBusy(true);
    try {
      const response = await call<{ job?: ReportJob; jobId?: string }>(`/v1/admin/reports/${detail.jobId}/${action}`, { method: "POST", headers: action === "retry" ? { "Idempotency-Key": crypto.randomUUID() } : undefined });
      const nextID = response.job?.jobId ?? response.jobId ?? detail.jobId;
      await Promise.all([refreshReports(), refreshHealth()]);
      setSelected(nextID);
      setMessage(action === "retry" ? `Retry ${nextID} queued` : `Report ${nextID} recovered`);
    } catch (error) { setMessage((error as Error).message); }
    finally { setBusy(false); }
  }
  const field = (key: keyof typeof filters, label: string, type = "text") => <label>{label}<input type={type} value={filters[key]} onChange={(event) => setFilters({ ...filters, [key]: event.target.value })} /></label>;
  return <section className="reports-workspace">
    <header className="reports-hero"><div><div className="eyebrow">Administration / Reports</div><h1>Report operations</h1><p>Trace immutable inputs, rendering attempts, workers, and queue delivery.</p></div><button className="secondary" onClick={() => Promise.all([refreshReports(), refreshHealth(), refreshDetail()]).catch((error: Error) => setMessage(error.message))}>Refresh</button></header>
    {message && <div className="storage-message">{message}</div>}
    <section className="ops-strip">
      {(["queued", "processing", "completed", "failed"] as const).map((status) => <button key={status} onClick={() => { const next = { ...filters, status }; setFilters(next); setApplied(next); setOffset(0); }}><strong>{health?.database.reportCounts[status] ?? "-"}</strong><span>{status}</span></button>)}
      <div><strong>{health?.database.outboxPending ?? "-"}</strong><span>outbox pending</span></div>
      <div><strong>{health?.database.oldestQueuedAgeSeconds != null ? `${health.database.oldestQueuedAgeSeconds}s` : "-"}</strong><span>oldest queued</span></div>
      <div><strong>{health?.database.migrationsPending ?? "-"}</strong><span>migrations pending</span></div>
      <div><strong>{health?.database.migrationsRunning ?? "-"}</strong><span>migrations running</span></div>
    </section>
    <section className="health-panel">
      <div className="panel-title"><div><span>Workers & queues</span><small>30 second online threshold</small></div></div>
      <div className="health-items">{health?.database.workers.map((worker) => <article key={worker.workerId}><i className={worker.online ? "online" : ""} /><div><strong>{worker.workerId}</strong><small>{worker.rendererVersion} / {worker.currentJobs} of {worker.concurrency} jobs / seen {worker.ageSeconds}s ago</small></div></article>)}
      {health?.database.workers.length === 0 && <span className="empty-state">No worker heartbeats recorded.</span>}
      {health?.queues.map((queue) => <article key={queue.queue}><i className={!queue.paused ? "online" : ""} /><div><strong>{queue.queue}</strong><small>{queue.active} active / {queue.pending} pending / {queue.retry} retry / {queue.archived} archived</small></div></article>)}</div>
    </section>
    <form className="report-filters" onSubmit={(event) => { event.preventDefault(); setOffset(0); setApplied(filters); }}>
      {field("sampleId", "Sample ID contains")}{field("template", "Template slug")}{field("templateVersion", "Exact version", "number")}
      <label>Status<select value={filters.status} onChange={(event) => setFilters({ ...filters, status: event.target.value })}><option value="">Any</option><option>queued</option><option>processing</option><option>completed</option><option>failed</option></select></label>
      {field("requestedBy", "Requested identity")}{field("createdFrom", "Created from", "datetime-local")}{field("createdTo", "Created to", "datetime-local")}
      <button className="primary">Apply filters</button>
    </form>
    <div className="reports-layout">
      <section className="report-list"><div className="report-list-head"><span>Report</span><span>Created / actor</span><span>Status</span></div>{result.items.map((report) => <button className={selected === report.jobId ? "active" : ""} key={report.jobId} onClick={() => setSelected(report.jobId)}><div><strong>{report.template} <small>v{report.templateVersion}</small></strong><code>{report.jobId}</code></div><div><span>{new Date(report.createdAt).toLocaleString()}</span><small>{report.requestedBy || "unknown"}</small></div><ReportStatus status={report.status} /></button>)}{result.items.length === 0 && <div className="empty-state">No reports match these filters.</div>}<footer><button disabled={offset === 0} onClick={() => refreshReports(Math.max(0, offset - pageSize))}>Previous</button><span>{offset + 1}-{offset + result.items.length}</span><button disabled={!result.hasMore} onClick={() => refreshReports(offset + pageSize)}>Next</button></footer></section>
      <section className="report-detail">{detail ? <><header><div><div className="eyebrow">{detail.template} / v{detail.templateVersion}</div><h2>{detail.jobId}</h2></div><div className="report-actions">{detail.downloadUrl && <a href={detail.downloadUrl}>Download PDF</a>}{detail.status === "failed" && <button className="primary" disabled={busy} onClick={() => operate("retry")}>Retry as new report</button>}{detail.status === "processing" && <button className="secondary danger" disabled={busy} onClick={() => operate("recover")}>Recover stale</button>}</div></header>
        <div className="report-metadata"><Metadata label="Status" value={detail.status} /><Metadata label="Requested by" value={detail.requestedBy} /><Metadata label="Data SHA-256" value={detail.dataSha256} /><Metadata label="PDF SHA-256" value={detail.sha256} /><Metadata label="Schema hash" value={detail.schemaHash} /><Metadata label="Storage profile" value={detail.storageProfile} /><Metadata label="Renderer" value={detail.rendererVersion} /><Metadata label="Attempts" value={String(detail.attempts)} /><Metadata label="Retry parent" value={detail.retryParentId} /><Metadata label="Retry children" value={detail.retryChildren.join(", ") || undefined} /><Metadata label="Started" value={detail.startedAt && new Date(detail.startedAt).toLocaleString()} /><Metadata label="Completed" value={detail.completedAt && new Date(detail.completedAt).toLocaleString()} /></div>
        {detail.error && <div className="report-error"><strong>Last error</strong><pre>{detail.error}</pre></div>}
        <div className="attempts"><div className="panel-title"><div><span>Attempt timeline</span><small>Append-only audit</small></div></div>{detail.attemptTimeline.map((event) => <article key={event.id}><i className={`event-${event.type}`} /><div><strong>{event.type} <span>attempt {event.attempt}</span></strong><small>{event.workerId || event.actor} / {new Date(event.occurredAt).toLocaleString()}</small>{event.message && <p>{event.message}</p>}</div></article>)}{detail.attemptTimeline.length === 0 && <div className="empty-state">No worker attempt has started.</div>}</div>
      </> : <div className="empty-state">Select a report to inspect its immutable metadata.</div>}</section>
    </div>
  </section>;
}

function ReportStatus({ status }: { status: ReportJob["status"] }) { return <span className={`report-status ${status}`}>{status}</span>; }
function Metadata({ label, value }: { label: string; value?: string }) { return <dl><dt>{label}</dt><dd title={value}>{value || "-"}</dd></dl>; }

type StorageProfile = { id: string; name: string; backendType: string; state: string; credentialConfigured: boolean; requestRateLimit?: number; transferConcurrency?: number };
type StorageMigration = { id: string; state: string; totalItems: number; completedItems: number; failedItems: number };

function StorageArea() {
  const [profiles, setProfiles] = useState<StorageProfile[]>([]);
  const [migrations, setMigrations] = useState<StorageMigration[]>([]);
  const [defaultID, setDefaultID] = useState("");
  const [source, setSource] = useState("");
  const [destination, setDestination] = useState("");
  const [message, setMessage] = useState("");
  const [showProfile, setShowProfile] = useState(false);
  async function call<T>(path: string, options: RequestInit = {}) { const response = await fetch(path, { ...options, headers: options.body ? { "Content-Type": "application/json" } : undefined }); const body = response.status === 204 ? null : await response.json().catch(() => null); if (!response.ok) throw new Error(body?.error?.message ?? "Request failed"); return body as T; }
  async function refresh() { const [nextProfiles, nextMigrations, current] = await Promise.all([call<StorageProfile[]>("/v1/storage/profiles"), call<StorageMigration[]>("/v1/storage/migrations"), call<StorageProfile>("/v1/storage/default")]); setProfiles(nextProfiles); setMigrations(nextMigrations); setDefaultID(current.id); if (!source && nextProfiles[0]) setSource(nextProfiles[0].id); if (!destination && nextProfiles[1]) setDestination(nextProfiles[1].id); }
  useEffect(() => { refresh().catch((error: Error) => setMessage(error.message)); const timer = window.setInterval(() => refresh().catch(() => {}), 5000); return () => window.clearInterval(timer); }, []);
  async function action(path: string, body?: unknown) { try { await call(path, { method: "POST", body: body ? JSON.stringify(body) : undefined }); await refresh(); setMessage("Storage operation accepted"); } catch (error) { setMessage((error as Error).message); } }
  return <section className="storage-workspace"><header className="storage-hero"><div><div className="eyebrow">Administration / Storage</div><h1>Overlapping storage</h1><p>Copy verified artifacts first. Cut over new writes separately when the destination is ready.</p></div><button className="primary" onClick={() => setShowProfile(true)}>Add S3 profile</button></header>{message && <div className="storage-message">{message}</div>}<div className="storage-grid"><section className="storage-panel"><div className="panel-title"><div><span>Profiles</span><small>Immutable destinations</small></div></div>{profiles.map((profile) => <article className="profile-row" key={profile.id}><div><strong>{profile.name}</strong><code>{profile.backendType} / {profile.id}</code></div><div className="profile-actions">{profile.id === defaultID ? <span className="default-pill">Default</span> : <button onClick={async () => { try { await call("/v1/storage/default", { method: "PUT", body: JSON.stringify({ profileId: profile.id }) }); await refresh(); } catch (error) { setMessage((error as Error).message); } }}>Set default</button>}<button onClick={() => action(`/v1/storage/profiles/${profile.id}/test`)}>Test</button></div><dl><dt>Rate</dt><dd>{profile.requestRateLimit ?? "unlimited"}/s</dd><dt>Concurrency</dt><dd>{profile.transferConcurrency ?? 1}</dd><dt>Credentials</dt><dd>{profile.credentialConfigured ? "configured" : "runtime"}</dd></dl></article>)}</section><section className="storage-panel"><div className="panel-title"><div><span>New migration</span><small>Snapshot available source placements</small></div></div><div className="migration-form"><label>Source<select value={source} onChange={(event) => setSource(event.target.value)}>{profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label><label>Destination<select value={destination} onChange={(event) => setDestination(event.target.value)}>{profiles.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</select></label><button className="primary" disabled={!source || !destination || source === destination} onClick={() => action("/v1/storage/migrations", { sourceProfileId: source, destinationProfileId: destination })}>Create snapshot</button></div></section></div><section className="storage-panel migrations"><div className="panel-title"><div><span>Migrations</span><small>Source data is never deleted</small></div></div>{migrations.length === 0 && <p className="empty-state">No migrations created.</p>}{migrations.map((migration) => { const percent = migration.totalItems ? Math.round(migration.completedItems / migration.totalItems * 100) : 100; return <article className="migration-row" key={migration.id}><div className="migration-id"><strong>{migration.state}</strong><code>{migration.id}</code></div><div className="progress"><i style={{ width: `${percent}%` }} /><span>{migration.completedItems} / {migration.totalItems} verified{migration.failedItems ? `, ${migration.failedItems} failed` : ""}</span></div><div className="migration-actions">{migration.state === "running" && <button onClick={() => action(`/v1/storage/migrations/${migration.id}/pause`)}>Pause</button>}{["draft", "paused", "failed"].includes(migration.state) && <button className="primary" onClick={() => action(`/v1/storage/migrations/${migration.id}/resume`)}>Start / resume</button>}{!["completed", "canceled"].includes(migration.state) && <button className="danger secondary" onClick={() => action(`/v1/storage/migrations/${migration.id}/cancel`)}>Cancel</button>}</div></article>; })}</section>{showProfile && <NewStorageProfile onClose={() => setShowProfile(false)} onCreated={async () => { setShowProfile(false); await refresh(); }} />}</section>;
}

function NewStorageProfile({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [form, setForm] = useState({ name: "", endpoint: "", bucket: "", region: "auto", accessKeyId: "", secretAccessKey: "", requestRateLimit: "2", transferConcurrency: "2" }); const [error, setError] = useState("");
  async function submit(event: React.FormEvent) { event.preventDefault(); const body = { name: form.name, backendType: "s3", publicConfig: { endpoint: form.endpoint, bucket: form.bucket, region: form.region, useSSL: true }, credentials: { accessKeyId: form.accessKeyId, secretAccessKey: form.secretAccessKey }, requestRateLimit: form.requestRateLimit ? Number(form.requestRateLimit) : null, transferConcurrency: Number(form.transferConcurrency) }; const response = await fetch("/v1/storage/profiles", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); const result = await response.json(); if (!response.ok) { setError(result.error?.message ?? "Could not create profile"); return; } onCreated(); }
  const field = (name: keyof typeof form, label: string, type = "text", help?: string) => <label>{label}<input type={type} required={name !== "requestRateLimit"} value={form[name]} onChange={(event) => setForm({ ...form, [name]: event.target.value })} />{help && <small className="field-help">{help}</small>}</label>;
  return <div className="modal-backdrop"><form className="modal storage-modal" onSubmit={submit}><div className="eyebrow">Immutable S3 profile</div><h2>Connect storage</h2>{field("name", "Profile name")}{field("endpoint", "Endpoint")}{field("bucket", "Bucket")}<div className="form-columns">{field("region", "Region")}{field("transferConcurrency", "Concurrent transfers", "number", "Objects copied at once. Start with 2; use 1 for the lowest bucket impact.")}</div><div className="form-columns">{field("accessKeyId", "Access key ID")}{field("secretAccessKey", "Secret access key", "password")}</div>{field("requestRateLimit", "Transfer starts per second", "number", "Global across workers. Start with 2; leave blank for no start-rate limit.")}<p className="modal-note">These limits apply to background migration copies, not normal report generation. A temporary object is written and deleted to verify bucket access before encrypted credentials are persisted.</p>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button className="primary">Test and create</button></div></form></div>;
}

const apiScopes = ["templates:read", "reports:submit", "reports:read", "reports:download"] as const;
type APIClient = { id: string; name: string; description: string; enabled: boolean; scopes: string[]; createdAt: string; createdBy: string; disabledAt?: string };
type APIKey = { id: string; clientId: string; label: string; prefix: string; scopes: string[]; issuedAt: string; expiresAt?: string; revokedAt?: string; lastUsedAt?: string; lastUsedIp?: string; status: "active" | "expired" | "revoked" };
type IssuedAPIKey = APIKey & { secret: string };

function APIClientsArea() {
  const [clients, setClients] = useState<APIClient[]>([]);
  const [selected, setSelected] = useState("");
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [message, setMessage] = useState("");
  const [creating, setCreating] = useState(false);
  const [keyAction, setKeyAction] = useState<{ mode: "issue" | "rotate"; keyId?: string } | null>(null);
  const [issuedSecret, setIssuedSecret] = useState("");

  async function call<T>(path: string, options: RequestInit = {}) {
    const response = await fetch(path, { ...options, headers: options.body ? { "Content-Type": "application/json" } : undefined });
    const body = response.status === 204 ? null : await response.json().catch(() => null);
    if (!response.ok) throw new Error(body?.error?.message ?? "Request failed");
    return body as T;
  }
  async function refreshClients() {
    const next = await call<APIClient[]>("/v1/api-clients");
    setClients(next);
    setSelected((current) => current || next[0]?.id || "");
  }
  async function refreshKeys(clientID = selected) {
    if (!clientID) { setKeys([]); return; }
    setKeys(await call<APIKey[]>(`/v1/api-clients/${clientID}/keys`));
  }
  useEffect(() => { refreshClients().catch((error: Error) => setMessage(error.message)); }, []);
  useEffect(() => { refreshKeys().catch((error: Error) => setMessage(error.message)); }, [selected]);
  const client = clients.find((item) => item.id === selected);

  async function disable() {
    if (!client || !window.confirm(`Disable ${client.name}? Every key will stop working immediately.`)) return;
    try { await call(`/v1/api-clients/${client.id}/disable`, { method: "POST" }); await refreshClients(); setMessage("Client disabled; all keys are invalid"); }
    catch (error) { setMessage((error as Error).message); }
  }
  async function revoke(key: APIKey) {
    if (!window.confirm(`Revoke ${key.label}? This cannot be undone.`)) return;
    try { await call(`/v1/api-clients/${key.clientId}/keys/${key.id}/revoke`, { method: "POST" }); await refreshKeys(); setMessage("Key revoked"); }
    catch (error) { setMessage((error as Error).message); }
  }

  return <section className="clients-workspace">
    <header className="clients-hero"><div><div className="eyebrow">Administration / API Clients</div><h1>Machine credentials</h1><p>Grant only the report capabilities each integration needs. Keys retain a snapshot of these scopes.</p></div><button className="primary" onClick={() => setCreating(true)}>Create client</button></header>
    {message && <div className="storage-message">{message}</div>}
    <div className="clients-layout">
      <aside className="clients-list">{clients.map((item) => <button key={item.id} className={item.id === selected ? "active" : ""} onClick={() => setSelected(item.id)}><strong>{item.name}</strong><span>{item.enabled ? `${item.scopes.length} scopes` : "Disabled"}</span></button>)}{clients.length === 0 && <div className="empty-state">No API clients yet.</div>}</aside>
      <section className="client-detail">{client ? <>
        <header><div><h2>{client.name}</h2><p>{client.description || "No description"}</p></div><div className="client-actions"><span className={client.enabled ? "client-state enabled" : "client-state"}>{client.enabled ? "Enabled" : "Disabled"}</span>{client.enabled && <button className="secondary danger" onClick={disable}>Disable client</button>}<button className="primary" disabled={!client.enabled} onClick={() => setKeyAction({ mode: "issue" })}>Issue key</button></div></header>
        <div className="scope-row">{client.scopes.map((scope) => <code key={scope}>{scope}</code>)}</div>
        <div className="key-table"><div className="key-table-head"><span>Key</span><span>Status / expiry</span><span>Last use</span><span /></div>{keys.map((key) => <article key={key.id}><div><strong>{key.label}</strong><code>{key.prefix}...</code></div><div><span className={`key-status ${key.status}`}>{key.status}</span><small>{key.expiresAt ? new Date(key.expiresAt).toLocaleString() : "No expiry"}</small></div><div><span>{key.lastUsedAt ? new Date(key.lastUsedAt).toLocaleString() : "Never"}</span><small>{key.lastUsedIp || ""}</small></div><div className="key-actions">{key.status === "active" && <><button onClick={() => setKeyAction({ mode: "rotate", keyId: key.id })}>Rotate</button><button className="danger" onClick={() => revoke(key)}>Revoke</button></>}</div></article>)}</div>
      </> : <div className="empty-state">Select or create a client.</div>}</section>
    </div>
    {creating && <NewAPIClient onClose={() => setCreating(false)} onCreated={async (created) => { setCreating(false); await refreshClients(); setSelected(created.id); }} />}
    {keyAction && client && <APIKeyDialog client={client} action={keyAction} onClose={() => setKeyAction(null)} onIssued={async (issued) => { setKeyAction(null); setIssuedSecret(issued.secret); await refreshKeys(client.id); }} />}
    {issuedSecret && <div className="modal-backdrop"><section className="modal secret-modal"><div className="eyebrow">Credential issued once</div><h2>Store this key now</h2><p className="secret-warning">This plaintext cannot be retrieved again. Put it directly in the integration's secret manager. Do not paste it into tickets, logs, or source control.</p><code className="issued-secret">{issuedSecret}</code><div className="modal-actions"><button className="secondary" onClick={() => navigator.clipboard.writeText(issuedSecret)}>Copy key</button><button className="primary" onClick={() => setIssuedSecret("")}>I have stored it</button></div></section></div>}
  </section>;
}

function NewAPIClient({ onClose, onCreated }: { onClose: () => void; onCreated: (client: APIClient) => void }) {
  const [name, setName] = useState(""); const [description, setDescription] = useState(""); const [scopes, setScopes] = useState<string[]>([]); const [error, setError] = useState("");
  async function submit(event: React.FormEvent) { event.preventDefault(); const response = await fetch("/v1/api-clients", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, description, scopes }) }); const body = await response.json(); if (!response.ok) { setError(body.error?.message ?? "Could not create client"); return; } onCreated(body as APIClient); }
  return <div className="modal-backdrop"><form className="modal client-modal" onSubmit={submit}><div className="eyebrow">New machine identity</div><h2>Create API client</h2><label>Name<input autoFocus required maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /></label><label>Description<textarea maxLength={1000} value={description} onChange={(event) => setDescription(event.target.value)} /></label><fieldset><legend>Scopes</legend>{apiScopes.map((scope) => <label key={scope}><input type="checkbox" checked={scopes.includes(scope)} onChange={(event) => setScopes(event.target.checked ? [...scopes, scope] : scopes.filter((item) => item !== scope))} /><span><strong>{scope}</strong><small>{scope === "templates:read" ? "Read the published template catalog" : scope === "reports:submit" ? "Create report jobs" : scope === "reports:read" ? "Read report jobs and lists" : "Download completed PDFs"}</small></span></label>)}</fieldset>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button className="primary" disabled={!name.trim() || scopes.length === 0}>Create client</button></div></form></div>;
}

function APIKeyDialog({ client, action, onClose, onIssued }: { client: APIClient; action: { mode: "issue" | "rotate"; keyId?: string }; onClose: () => void; onIssued: (key: IssuedAPIKey) => void }) {
  const [label, setLabel] = useState(""); const [expiresAt, setExpiresAt] = useState(""); const [graceMinutes, setGraceMinutes] = useState("15"); const [error, setError] = useState("");
  async function submit(event: React.FormEvent) { event.preventDefault(); const rotating = action.mode === "rotate"; const path = rotating ? `/v1/api-clients/${client.id}/keys/${action.keyId}/rotate` : `/v1/api-clients/${client.id}/keys`; const body = rotating ? { label, graceSeconds: Number(graceMinutes) * 60 } : { label, expiresAt: expiresAt ? new Date(expiresAt).toISOString() : null }; const response = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); const result = await response.json(); if (!response.ok) { setError(result.error?.message ?? "Could not issue key"); return; } onIssued(result as IssuedAPIKey); }
  return <div className="modal-backdrop"><form className="modal" onSubmit={submit}><div className="eyebrow">{action.mode === "rotate" ? "Overlapping rotation" : client.name}</div><h2>{action.mode === "rotate" ? "Rotate key" : "Issue API key"}</h2><label>Display label<input autoFocus required maxLength={120} value={label} onChange={(event) => setLabel(event.target.value)} placeholder="Production LIMS" /></label>{action.mode === "issue" ? <label>Expiry (optional)<input type="datetime-local" value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} /></label> : <label>Old key grace period (minutes)<input type="number" min="0" max="43200" required value={graceMinutes} onChange={(event) => setGraceMinutes(event.target.value)} /></label>}<p className="modal-note">The new key snapshots: {client.scopes.join(", ")}.</p>{error && <p className="form-error">{error}</p>}<div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button className="primary">{action.mode === "rotate" ? "Rotate and reveal" : "Issue and reveal"}</button></div></form></div>;
}

function NewTemplate({ onClose, onCreated }: { onClose: () => void; onCreated: (version: TemplateVersion) => void }) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [error, setError] = useState("");

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    const response = await fetch("/v1/templates", {
      method: "POST",
      headers,
      body: JSON.stringify({ name, slug, source: starterSource, sampleData: JSON.parse(starterData) }),
    });
    const body = await response.json() as TemplateVersion & { error?: { message: string } };
    if (!response.ok) {
      setError(body.error?.message ?? "Could not create template");
      return;
    }
    onCreated(body);
  }

  return <div className="modal-backdrop"><form className="modal" onSubmit={submit}>
    <div className="eyebrow">New template</div><h2>Start a report</h2>
    <label>Name<input autoFocus value={name} onChange={(event) => { setName(event.target.value); setSlug(event.target.value.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")); }} /></label>
    <label>Slug<input value={slug} onChange={(event) => setSlug(event.target.value)} pattern="[a-z0-9]+(?:-[a-z0-9]+)*" /></label>
    {error && <p className="form-error">{error}</p>}
    <div className="modal-actions"><button type="button" className="secondary" onClick={onClose}>Cancel</button><button className="primary">Create draft</button></div>
  </form></div>;
}

export default App;
