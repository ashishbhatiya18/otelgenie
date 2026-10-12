package engine

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/go-pdf/fpdf"
)

const (
	pasteLines = 45
	sawLines   = 60
)

func clip(text string, max int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:max], "\n") + fmt.Sprintf("\n... %d more lines in the bundle ...", len(lines)-max)
}

// RenderPDF lays the report out for reading: the verdict and findings first,
// then every step with what the user saw and what they entered.
func RenderPDF(r *Report) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetMargins(16, 16, 16)
	pdf.SetAutoPageBreak(true, 16)
	pdf.SetTitle("otelgenie session "+r.Meta.SessionCode, false)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-11)
		pdf.SetFont("Helvetica", "", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 5, tr(fmt.Sprintf("otelgenie session %s    page %d", r.Meta.SessionCode, pdf.PageNo())), "", 0, "C", false, 0, "")
	})
	const width = 178.0

	heading := func(text string) {
		pdf.Ln(3)
		pdf.SetFont("Helvetica", "B", 13)
		pdf.SetTextColor(20, 20, 20)
		pdf.MultiCell(width, 7, tr(text), "", "L", false)
		pdf.Ln(1)
	}
	para := func(text string) {
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(40, 40, 40)
		pdf.MultiCell(width, 5, tr(text), "", "L", false)
	}
	label := func(k, v string) {
		pdf.SetFont("Helvetica", "B", 9)
		pdf.SetTextColor(90, 90, 90)
		pdf.CellFormat(42, 5, tr(k), "", 0, "L", false, 0, "")
		pdf.SetFont("Helvetica", "", 10)
		pdf.SetTextColor(30, 30, 30)
		pdf.MultiCell(width-42, 5, tr(v), "", "L", false)
	}
	mono := func(text string, fill [3]int) {
		if strings.TrimSpace(text) == "" {
			return
		}
		pdf.SetFont("Courier", "", 7.5)
		pdf.SetTextColor(30, 30, 30)
		pdf.SetFillColor(fill[0], fill[1], fill[2])
		pdf.MultiCell(width, 3.6, tr(text), "", "L", true)
		pdf.Ln(1.5)
	}
	tag := func(sev string) {
		colour := map[string][3]int{"problem": {190, 40, 40}, "warning": {180, 110, 0}, "info": {70, 100, 150}}[sev]
		pdf.SetFont("Helvetica", "B", 8)
		pdf.SetTextColor(255, 255, 255)
		pdf.SetFillColor(colour[0], colour[1], colour[2])
		pdf.CellFormat(20, 5, strings.ToUpper(sev), "", 0, "C", true, 0, "")
		pdf.SetTextColor(30, 30, 30)
	}
	grey := [3]int{244, 244, 244}
	blue := [3]int{236, 242, 250}

	pdf.AddPage()
	pdf.SetFont("Helvetica", "B", 18)
	pdf.MultiCell(width, 9, tr("Session replay: "+r.Inputs.Project+" / "+r.Inputs.Env), "", "L", false)
	pdf.Ln(2)

	heading("Where it went wrong")
	pdf.SetFont("Helvetica", "B", 11)
	pdf.SetTextColor(150, 30, 30)
	if strings.HasPrefix(r.Verdict, "Every server") {
		pdf.SetTextColor(30, 110, 50)
	}
	pdf.MultiCell(width, 6, tr(r.Verdict), "", "L", false)
	pdf.Ln(2)
	for _, t := range r.Targets {
		label(t.Name, t.Status)
	}

	heading("About this recording")
	label("Session code", r.Meta.SessionCode)
	integrity := "Complete and unedited"
	if !r.ChainOK {
		integrity = "DAMAGED: " + r.ChainProblem
	} else if !r.CodeMatches {
		integrity = "Does not match the code the user gave"
	}
	label("Integrity", integrity)
	rerun := "Identical: the tool did what it was built to do"
	if r.Rerun.Error != "" {
		rerun = "Could not be re-run: " + r.Rerun.Error
	} else if !r.Rerun.Same {
		rerun = fmt.Sprintf("DIFFERENT in %d place(s): see findings", len(r.Rerun.Diffs))
	}
	label("Re-run of inputs", rerun)
	label("Recorded events", fmt.Sprint(r.Meta.Events))
	label("Exported", r.Meta.ExportedAt)
	label("App version", r.Meta.AppVersion)
	label("Browser", r.Meta.Browser)

	heading("What the user provided")
	label("Project", r.Inputs.Project)
	label("Environment", r.Inputs.Env)
	label("Collector endpoint", r.Inputs.Endpoint)
	label("Telemetry", strings.Join(r.Inputs.Signals, ", "))
	label("Components", strings.Join(r.Inputs.Components, ", "))

	heading("Their one-time setup")
	label("Fingerprint", r.Meta.Fingerprint.String())
	if len(r.ReviewerDiff) > 0 {
		label("Differs from yours", strings.Join(r.ReviewerDiff, ", "))
	}
	label("Template", r.Template)
	label("Packages arrive by", r.Profile.PackageMethod+" "+r.Profile.PackageRepoURL)
	label("Images arrive by", r.Profile.ImageMethod+" "+r.Profile.Registry)
	ca := "none"
	if strings.TrimSpace(r.Profile.CABundle) != "" {
		ca = "provided"
	}
	label("Private CA", ca)
	label("Server types", strings.Join(r.Profile.OSFamilies, ", "))

	heading(fmt.Sprintf("Findings (%d)", len(r.Findings)))
	if len(r.Findings) == 0 {
		para("Nothing unusual was recorded.")
	}
	for _, f := range r.Findings {
		tag(f.Severity)
		where := ""
		if f.Target != "" {
			where = "  [" + f.Target
			if f.Step != "" {
				where += ", " + stepTitles[f.Step]
			}
			where += "]"
		}
		if f.Seq > 0 {
			where += fmt.Sprintf("  event %d", f.Seq)
		}
		pdf.SetFont("Helvetica", "B", 10)
		pdf.MultiCell(width-20, 5, tr(" "+f.Title+where), "", "L", false)
		if f.Detail != "" {
			pdf.SetFont("Helvetica", "", 9)
			pdf.SetTextColor(60, 60, 60)
			pdf.MultiCell(width, 4.4, tr(clip(f.Detail, 14)), "", "L", false)
		}
		pdf.Ln(2)
	}

	pdf.AddPage()
	heading("The session, step by step")
	para("Each entry is one thing the user did. Grey is what they entered; blue is the screen they were shown next.")
	for _, s := range r.Steps {
		pdf.Ln(3)
		if s.Gap != "" {
			pdf.SetFont("Helvetica", "I", 9)
			pdf.SetTextColor(120, 120, 120)
			pdf.MultiCell(width, 5, tr("--- "+s.Gap+" ---"), "", "C", false)
		}
		pdf.SetFont("Helvetica", "B", 10.5)
		pdf.SetTextColor(20, 20, 20)
		title := fmt.Sprintf("%d. %s", s.Seq, s.Action)
		if s.Target != "" {
			title += " [" + s.Target + "]"
		}
		pdf.MultiCell(width, 5.5, tr(title), "", "L", false)
		pdf.SetFont("Helvetica", "", 8.5)
		pdf.SetTextColor(110, 110, 110)
		at := s.TS
		if s.Step != "" && stepTitles[s.Step] != "" {
			at += "    at step: " + stepTitles[s.Step]
		}
		pdf.MultiCell(width, 4.5, tr(at), "", "L", false)
		mono(clip(s.Input, pasteLines), grey)
		if s.Rejected != "" {
			pdf.SetFont("Helvetica", "B", 9.5)
			pdf.SetTextColor(190, 40, 40)
			pdf.MultiCell(width, 5, tr("Not accepted: "+s.Rejected), "", "L", false)
		}
		pdf.SetFont("Helvetica", "", 9.5)
		pdf.SetTextColor(40, 40, 40)
		for _, d := range s.Decisions {
			pdf.MultiCell(width, 4.8, tr("- "+d), "", "L", false)
		}
		if s.Outcome != "" {
			pdf.SetFont("Helvetica", "B", 9.5)
			pdf.MultiCell(width, 5, tr(s.Outcome), "", "L", false)
		}
		if len(s.Files) > 0 {
			var parts []string
			for _, f := range s.Files {
				parts = append(parts, f.Path+" ("+f.Change+")")
			}
			pdf.SetFont("Helvetica", "", 8.5)
			pdf.SetTextColor(90, 90, 90)
			pdf.MultiCell(width, 4.4, tr("Files: "+strings.Join(parts, ", ")), "", "L", false)
		}
		mono(clip(s.Saw, sawLines), blue)
	}

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
