package api

import (
	"net/http/httptest"
	"testing"
)

func TestReportListFilterValidation(t *testing.T) {
	tests := []struct {
		query string
	}{
		{query: "templateVersion=0"},
		{query: "templateVersion=nope"},
		{query: "createdFrom=2026-01-01"},
		{query: "createdFrom=2026-02-01T00:00:00Z&createdTo=2026-01-01T00:00:00Z"},
	}
	for _, test := range tests {
		request := httptest.NewRequest("GET", "/v1/reports?"+test.query, nil)
		if _, err := reportListFilter(request, 50, 0, ""); err == nil {
			t.Errorf("reportListFilter(%q) accepted invalid input", test.query)
		}
	}
}

func TestReportListFilterIncludesOperatorFields(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/reports?template=coa&templateVersion=3&sampleId=2600&requestedBy=admin%40example.test&createdFrom=2026-01-01T00:00:00Z&createdTo=2026-02-01T00:00:00Z", nil)
	filter, err := reportListFilter(request, 25, 50, "failed")
	if err != nil {
		t.Fatal(err)
	}
	if filter.Template != "coa" || filter.TemplateVersion == nil || *filter.TemplateVersion != 3 || filter.SampleID != "2600" || filter.RequestedBy != "admin@example.test" || filter.Status != "failed" || filter.Limit != 25 || filter.Offset != 50 {
		t.Fatalf("unexpected filter: %#v", filter)
	}
	if filter.CreatedFrom == nil || filter.CreatedTo == nil {
		t.Fatal("expected both date boundaries")
	}
}
