package cli

import (
	"strconv"
	"strings"
	"unicode"

	"recall/internal/model"
)

// terminalText visibly escapes C0/DEL/C1 controls rather than interpreting or
// deleting their payloads. Only passages retain authored LF; table fields must
// not inject rows/tabs. JSON and explicit raw document output bypass this helper.
func terminalText(text string, passage bool) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) && !(passage && r == '\n') {
			quoted := strconv.QuoteRuneToASCII(r)
			out.WriteString(quoted[1 : len(quoted)-1])
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// Validate complete discovery data before writing either human or JSON output.
// Full documents/raw/history and mutation results deliberately remain exempt.
func checkDiscovery(data any) error {
	switch page := data.(type) {
	case documentList:
		if err := model.CheckDiagnostics(page.Diagnostics); err != nil {
			return err
		}
		if err := model.CheckFolders(page.Folders); err != nil {
			return err
		}
	case []string:
		if err := model.CheckFolders(page); err != nil {
			return err
		}
	case model.Page[model.DocumentSummary], []model.Hit:
	default:
		return nil
	}
	return model.CheckPreviewData(data)
}
