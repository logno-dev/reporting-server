#import "../common/lab.typ": *
#let data = json("report.json")

#lab-report(title: "Certificate of Analysis")
#section("Sample Information")
#field("Sample ID", data.sample.id)
#field("Description", data.sample.description)
#section("Results")
#results-table(data.results)
