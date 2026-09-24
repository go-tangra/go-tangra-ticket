package httpapi

// SanitizeFilename keeps a Content-Disposition filename header-safe.
func SanitizeFilename(name string) string {
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r == '"' || r == '\\' || r < 0x20 || r == 0x7f:
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "download"
	}
	return string(out)
}
