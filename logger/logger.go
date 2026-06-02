package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"

	"github.com/pterm/pterm"
	"github.com/pterm/pterm/putils"
)

// PrettyHandler is a pterm-powered slog handler.
type PrettyHandler struct {
	w     io.Writer
	level slog.Level
	mu    sync.Mutex
}

func NewPrettyHandler(w io.Writer, level slog.Level) *PrettyHandler {
	return &PrettyHandler{w: w, level: level}
}

func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	timeStr := pterm.Gray(r.Time.Format("15:04:05"))

	var prefix string
	switch {
	case r.Level >= slog.LevelError:
		prefix = pterm.Red("✖")
	case r.Level >= slog.LevelWarn:
		prefix = pterm.Yellow("▲")
	case r.Level >= slog.LevelInfo:
		prefix = pterm.Blue("●")
	default:
		prefix = pterm.Gray("○")
	}

	var msgColor func(a ...interface{}) string
	switch {
	case r.Level >= slog.LevelError:
		msgColor = pterm.Red
	case r.Level >= slog.LevelWarn:
		msgColor = pterm.Yellow
	case r.Level >= slog.LevelInfo:
		msgColor = pterm.White
	default:
		msgColor = pterm.Gray
	}

	line := fmt.Sprintf("%s %s %s", timeStr, prefix, msgColor(r.Message))

	// Append attributes
	r.Attrs(func(a slog.Attr) bool {
		val := fmt.Sprintf("%v", a.Value)
		if val != "" && val != "0" {
			line += pterm.Gray(" "+a.Key+"=") + pterm.White(val)
		}
		return true
	})

	fmt.Fprintln(h.w, line)
	return nil
}

func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	return h
}

// Banner prints the startup banner with server info.
func Banner(version, commit, listen, adminListen, region, storageDir, dbPath string, tlsEnabled, adminEnabled bool, webdavEnabled bool, webdavListen string) {
	// Big text header
	s, _ := pterm.DefaultBigText.WithLetters(
		putils.LettersFromStringWithStyle("Cloodsy", pterm.NewStyle(pterm.FgCyan, pterm.Bold)),
		putils.LettersFromStringWithStyle(" - ", pterm.NewStyle(pterm.FgGray, pterm.Bold)),
		putils.LettersFromStringWithStyle("S3", pterm.NewStyle(pterm.FgWhite, pterm.Bold)),
	).Srender()
	pterm.Println(s)

	// Version line
	versionStr := pterm.Bold.Sprintf("Cloodsy S3") + " " + pterm.Green("v"+version)
	if commit != "unknown" {
		versionStr += pterm.Gray(" (" + commit + ")")
	}
	pterm.Println("  " + versionStr)
	pterm.Println()

	// Info table
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	s3Addr := formatAddr(listen, scheme)

	tableData := [][]string{
		{"S3 API", s3Addr},
		{"Region", region},
		{"Storage", storageDir},
		{"Database", dbPath},
		{"Platform", runtime.GOOS + "/" + runtime.GOARCH},
	}

	if adminEnabled {
		// Insert after S3 API
		adminAddr := formatAddr(adminListen, "http")
		tableData = append([][]string{tableData[0], {"Admin API", adminAddr}}, tableData[1:]...)
	}

	if tlsEnabled {
		tableData = append(tableData, []string{"TLS", pterm.Green("enabled")})
	} else {
		tableData = append(tableData, []string{"TLS", pterm.Yellow("disabled")})
	}

	if webdavEnabled {
		webdavAddr := formatAddr(webdavListen, "http")
		tableData = append(tableData, []string{"WebDAV", pterm.Green("enabled") + " " + webdavAddr})
	} else {
		tableData = append(tableData, []string{"WebDAV", pterm.Gray("disabled")})
	}

	for _, row := range tableData {
		pterm.Printf("  %s  %s\n", pterm.Gray(padRight(row[0], 12)), pterm.White(row[1]))
	}

	pterm.Println()
	pterm.Println("  " + pterm.Cyan("cloodsy.com") + pterm.Gray(" • ") + pterm.Cyan("onaonbir.com"))
	pterm.Println()
}

func formatAddr(listen, scheme string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return fmt.Sprintf("%s://0.0.0.0%s", scheme, listen)
	}
	return fmt.Sprintf("%s://%s", scheme, listen)
}

func padRight(s string, length int) string {
	if len(s) >= length {
		return s
	}
	return s + strings.Repeat(" ", length-len(s))
}
