// PocketBase, built the way PocketBase recommends extending it: as a Go framework, with this file
// as the main package. It is the upstream "base" build (the same one as the official release
// binary: JS hooks, JS migrations, a public folder) plus what running on velixir needs:
//
//   1. Data on the volume. velixir sets VELIXIR_VOLUME_PATH when the app has one, and pb_data
//      goes there, so the database and uploaded files survive restarts and redeploys. Without
//      one, pb_data sits on the container disk and starts empty on every restart, and the log
//      says so in a box you cannot miss.
//   2. Listening on $PORT, on every interface, with no arguments needed.
//   3. No open installer. A fresh PocketBase prints a one-time link to create the first
//      superuser. This build creates the superuser itself instead and prints the password once
//      to the deploy log, or takes PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD from the environment.
//   4. The visitor's real address. The platform's edge passes it in X-Real-IP, and PocketBase
//      trusts that header from the first start, so logs and rate limits see visitors rather
//      than the proxy.
//   5. /healthz for the platform's health probe.
//
// The self-update command of the release binary is left out on purpose: it would replace a file
// on a disk that is rebuilt on every deploy. Upgrading is a version bump in go.mod.

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/jsvm"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"
	"github.com/pocketbase/pocketbase/tools/hook"
)

func main() {
	volume := strings.TrimSpace(os.Getenv("VELIXIR_VOLUME_PATH"))
	dataDir := filepath.Join(appDir(), "pb_data")
	if volume != "" {
		dataDir = filepath.Join(volume, "pb_data")
	}

	// No arguments (how the platform starts it): serve on $PORT. Any arguments are passed to
	// PocketBase unchanged, so `./out superuser upsert ...` and friends still work locally.
	if len(os.Args) == 1 {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			port = "8090"
		}
		os.Args = append(os.Args, "serve", "--http=0.0.0.0:"+port)
	}

	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: dataDir})

	// ---------------------------------------------------------------
	// The flags of the official release binary, with the same meaning.
	// ---------------------------------------------------------------

	var hooksDir string
	app.RootCmd.PersistentFlags().StringVar(&hooksDir, "hooksDir", filepath.Join(appDir(), "pb_hooks"),
		"the directory with the JS app hooks")

	var hooksPool int
	app.RootCmd.PersistentFlags().IntVar(&hooksPool, "hooksPool", 15,
		"the total prewarm goja.Runtime instances for the JS app hooks execution")

	var migrationsDir string
	app.RootCmd.PersistentFlags().StringVar(&migrationsDir, "migrationsDir", filepath.Join(appDir(), "pb_migrations"),
		"the directory with the user defined migrations")

	// Off by default here, unlike the release binary: migration files written while you edit
	// collections in the dashboard would land on the container disk and vanish on the next
	// deploy. The schema itself lives in the database on the volume. Commit migrations to
	// pb_migrations/ in this repository if you want them versioned.
	var automigrate bool
	app.RootCmd.PersistentFlags().BoolVar(&automigrate, "automigrate", envBool("PB_AUTOMIGRATE"),
		"enable/disable auto migrations")

	var publicDir string
	app.RootCmd.PersistentFlags().StringVar(&publicDir, "publicDir", filepath.Join(appDir(), "pb_public"),
		"the directory to serve static files")

	var indexFallback bool
	app.RootCmd.PersistentFlags().BoolVar(&indexFallback, "indexFallback", true,
		"fallback the request to index.html on missing static path, e.g. when pretty urls are used with SPA")

	app.RootCmd.ParseFlags(os.Args[1:])

	// ---------------------------------------------------------------
	// Plugins, as in the release binary.
	// ---------------------------------------------------------------

	jsvm.MustRegister(app, jsvm.Config{
		MigrationsDir: migrationsDir,
		HooksDir:      hooksDir,
		// The repository is the only place hooks change, and a change is a redeploy.
		HooksWatch:    false,
		HooksPoolSize: hooksPool,
	})

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		TemplateLang: migratecmd.TemplateLangJS,
		Automigrate:  automigrate,
		Dir:          migrationsDir,
	})

	// ---------------------------------------------------------------
	// velixir: settings, the first superuser, /healthz.
	// ---------------------------------------------------------------

	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		if err := applyPlatformSettings(e.App); err != nil {
			return err
		}
		created, err := ensureSuperuser(e.App)
		if err != nil {
			return err
		}
		if created {
			// The superuser exists, so the one-time installer link has nothing to do.
			e.InstallerFunc = nil
		}

		e.Router.GET("/healthz", func(re *core.RequestEvent) error {
			return re.String(http.StatusOK, "ok")
		})

		printVolumeNotice(volume, dataDir)
		return e.Next()
	})

	// Static files from pb_public/, served last so your own routes and hooks win.
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			if !e.Router.HasRoute(http.MethodGet, "/{path...}") {
				e.Router.GET("/{path...}", apis.Static(os.DirFS(publicDir), indexFallback))
			}
			return e.Next()
		},
		Priority: 999,
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// applyPlatformSettings fills in what PocketBase cannot know about velixir, once: the app's
// public URL (used in emails and OAuth2 redirects) and the trusted client-address header. Both
// are only set while they still have PocketBase's defaults, so a change you make in the
// dashboard is never overwritten.
func applyPlatformSettings(app core.App) error {
	settings := app.Settings()
	changed := false

	if url := publicURL(); url != "" && (settings.Meta.AppURL == "" || strings.Contains(settings.Meta.AppURL, "localhost")) {
		settings.Meta.AppURL = url
		changed = true
	}

	if len(settings.TrustedProxy.Headers) == 0 {
		// The edge sets X-Real-IP to the visitor's address and drops any copy the visitor sent.
		settings.TrustedProxy.Headers = []string{"X-Real-IP"}
		changed = true
	}

	if !changed {
		return nil
	}
	return app.Save(settings)
}

// ensureSuperuser makes sure nobody but the owner can claim a fresh instance.
//
// PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD, when both are set, create that superuser or reset its
// password on every start (the way back in if you lose it). Otherwise, the first start creates
// admin@<your app's host> with a random password and prints it once to the log. Returns true
// when a real superuser exists afterwards.
func ensureSuperuser(app core.App) (bool, error) {
	superusers, err := app.FindCachedCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return false, err
	}

	email := strings.TrimSpace(os.Getenv("PB_ADMIN_EMAIL"))
	password := os.Getenv("PB_ADMIN_PASSWORD")

	if email != "" && password != "" {
		if len(password) < 10 {
			return false, errors.New("PB_ADMIN_PASSWORD must be at least 10 characters")
		}
		record, err := app.FindAuthRecordByEmail(superusers, email)
		if err != nil {
			record = core.NewRecord(superusers)
			record.SetEmail(email)
		}
		record.SetPassword(password)
		if err := app.Save(record); err != nil {
			return false, fmt.Errorf("could not save the superuser from PB_ADMIN_EMAIL: %w", err)
		}
		removeInstallerSuperuser(app, superusers)
		log.Printf("PocketBase superuser %s set from PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD", email)
		return true, nil
	}

	existing, err := app.CountRecords(core.CollectionNameSuperusers, dbx.Not(dbx.HashExp{"email": core.DefaultInstallerEmail}))
	if err != nil {
		return false, err
	}
	if existing > 0 {
		return true, nil
	}

	email = "admin@" + adminDomain()
	password, err = randomPassword(24)
	if err != nil {
		return false, err
	}
	record := core.NewRecord(superusers)
	record.SetEmail(email)
	record.SetPassword(password)
	if err := app.Save(record); err != nil {
		return false, fmt.Errorf("could not create the first superuser: %w", err)
	}
	removeInstallerSuperuser(app, superusers)

	dashboard := publicURL()
	if dashboard == "" {
		dashboard = "http://localhost:" + envOr("PORT", "8090")
	}
	box([]string{
		"PocketBase is ready. Your superuser account:",
		"",
		"  Dashboard: " + strings.TrimRight(dashboard, "/") + "/_/",
		"  Email:     " + email,
		"  Password:  " + password,
		"",
		"This is shown once. Change the email and password in the dashboard",
		"(Superusers), or set PB_ADMIN_EMAIL and PB_ADMIN_PASSWORD on the",
		"Environment tab to reset them on the next start.",
	})
	return true, nil
}

// removeInstallerSuperuser deletes the temporary account PocketBase makes for its one-time
// installer link, if an earlier start left one behind.
func removeInstallerSuperuser(app core.App, superusers *core.Collection) {
	if record, err := app.FindAuthRecordByEmail(superusers, core.DefaultInstallerEmail); err == nil {
		_ = app.Delete(record)
	}
}

func printVolumeNotice(volume, dataDir string) {
	if volume != "" {
		log.Printf("PocketBase data is on the app's volume: %s", dataDir)
		return
	}
	box([]string{
		"No volume: PocketBase's data is on the container disk.",
		"",
		"The database and every uploaded file start empty again on each",
		"restart and redeploy, and so does the superuser account. Add a",
		"volume on the app's Volume tab (a paid plan) to keep them.",
	})
}

// publicURL is the app's address: PUBLIC_URL for a custom domain, else the velixir.run one.
func publicURL() string {
	if url := strings.TrimSpace(os.Getenv("PUBLIC_URL")); url != "" {
		return strings.TrimRight(url, "/")
	}
	if slug := strings.TrimSpace(os.Getenv("VELIXIR_APP_SLUG")); slug != "" {
		return "https://" + slug + ".velixir.run"
	}
	return ""
}

func adminDomain() string {
	url := publicURL()
	if url == "" {
		return "example.com"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexAny(host, ":/"); i >= 0 {
		host = host[:i]
	}
	return host
}

func randomPassword(n int) (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	out := make([]byte, n)
	for i := range out {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[v.Int64()]
	}
	return string(out), nil
}

func box(lines []string) {
	width := 0
	for _, l := range lines {
		if len(l) > width {
			width = len(l)
		}
	}
	border := "+" + strings.Repeat("-", width+2) + "+"
	var b strings.Builder
	b.WriteString("\n" + border + "\n")
	for _, l := range lines {
		b.WriteString("| " + l + strings.Repeat(" ", width-len(l)) + " |\n")
	}
	b.WriteString(border + "\n")
	fmt.Fprint(os.Stdout, b.String())
}

// appDir is the folder the binary lives in, where pb_hooks, pb_migrations and pb_public from
// this repository end up. `go run` builds into a temporary folder, so it falls back to the
// working directory there.
func appDir() string {
	exe, err := os.Executable()
	if err != nil || strings.Contains(exe, "go-build") {
		wd, _ := os.Getwd()
		return wd
	}
	return filepath.Dir(exe)
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
