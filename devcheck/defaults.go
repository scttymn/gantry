package devcheck

// DefaultRules returns a fresh registry. Add each new built-in feature's rule
// here: the CLI, browser, strict exit policy and exceptions use it automatically.
// Severity depends on detection confidence; the purpose states the benefit.
func DefaultRules() []Rule {
	return []Rule{
		{ID: "asset-url", Severity: Violation, Extensions: []string{".html", ".templ"}, Purpose: "Use Gantry's asset pipeline for cache invalidation, compression and reusable assets", Guidance: "local script or stylesheet uses a literal URL; use assets.Path/Styles/ImportMap so deployments replace cached copies; document an intentional alternative", Check: checkAssetURLs},
		{ID: "form-scope", Severity: Advice, Extensions: []string{".js"}, Purpose: "Reuse form-scoped behavior safely as shared navigation evolves", Guidance: "document.querySelector('form') can select a navigation form; use a form ID or a Stimulus controller scoped to that form", Check: checkForms},
		{ID: "shared-layout", Severity: Advice, Extensions: []string{".html", ".templ"}, Purpose: "Keep asset loading and page enhancements in the shared layout", Guidance: "standalone page shell outside app/shared; prefer the shared layout so navigation, assets and development diagnostics stay consistent", Check: checkLayout},
		{ID: "raw-assets", Severity: Advice, Extensions: []string{".go"}, Purpose: "Avoid rebuilding asset serving outside Gantry's optimized pipeline", Guidance: "raw file server bypasses Gantry asset handling; use assets.Routes for scripts/styles, or document this route's separate purpose", Check: checkRawAssets},
		{ID: "development-guard", Severity: Violation, Extensions: []string{".go"}, Purpose: "Keep development-only middleware (source scans, timing, reload) out of production", Guidance: developmentGuardGuidance(), Check: checkDevelopmentGuard},
		{ID: "environment-default", Severity: Violation, Extensions: []string{".go"}, Purpose: "Use Gantry's production defaults unless development is explicitly selected", Guidance: "GANTRY_ENV should default to production; opt into development explicitly", Check: checkEnvironment},
		{ID: "migration-down", Severity: Violation, Extensions: []string{".sql"}, Purpose: "Keep rollback and testkit.Migrations checking every migration's undo", Guidance: "migration has no -- +goose Down section, so rolling back stops at it; add one that undoes the up, or leave it empty to mark the migration irreversible", Check: checkMigrationDown},
	}
}
