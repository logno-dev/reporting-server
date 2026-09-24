#let ink = rgb("#17202a")
#let muted = rgb("#667085")
#let rule = rgb("#d0d5dd")
#let accent = rgb("#155eef")

#let lab-report(title: none) = {
  align(center)[
    #text(size: 18pt, weight: "bold", fill: ink)[#title]
    #v(4pt)
    #line(length: 100%, stroke: 1.5pt + accent)
  ]
  v(16pt)
}

#let section(title) = {
  v(12pt)
  text(size: 11pt, weight: "bold", fill: ink)[#title]
  v(5pt)
}

#let field(label, value) = {
  grid(
    columns: (32%, 1fr),
    gutter: 8pt,
    text(weight: "semibold", fill: muted)[#label],
    text(fill: ink)[#value],
  )
  v(3pt)
}

#let results-table(results) = {
  table(
    columns: (1fr, 1fr),
    inset: 7pt,
    stroke: rule,
    table.header(
      [*Test*], [*Result*],
    ),
    ..results.map(result => (
      [#result.test],
      [#result.result],
    )).flatten(),
  )
}
