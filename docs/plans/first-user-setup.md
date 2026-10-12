# First-install password account setup

User direction: first install only; require a one-time operator code; atomically
create the first account and permanently close setup. Keep existing apps closed.

Implementation: shared Auth opt-in (`FirstUser`); app-owned singleton migration;
random code on private persistent storage, only its digest in the database;
password generator uses shared Auth and generates an operator task. Existing
apps keep old code until deliberately migrated. Email-code mode is unaffected.

| Acceptance | Test |
| --- | --- |
| Fresh login shows setup; code never exposed in page or DB | TestFirstUserSetup |
| Password/email/code validation and CSRF | TestFirstUserSetup |
| Restarts keep pending code; explicit rotation invalidates it | TestFirstUserSetup |
| Account/session created, stale setup cannot create more users | TestFirstUserSetup |
| Deleting last user/restarting cannot reopen setup | TestFirstUserSetup |
| Competing processes/DB connections create one account | TestFirstUserConcurrency |
| Failed app hook rolls back account and completion | TestFirstUserFailureAndExistingInstall |
| Seeded users, existing-install migration, missing state fail closed | TestFirstUserFailureAndExistingInstall |
| Generated app views, sign-in/out, setup and Strict | TestEmailCodeRecipe password cases, TestAuthBuilds |

Strict cannot establish whether arbitrary custom setup code is intentional or
safe. Adoption is through the shared default generator, migration guidance and
integration tests; review custom flows against these cases. No speculative
static check is added.

Houston integration: `houston exec --server /app task auth:setup-code` uses
Houston's existing SSH connection and streams the result to the operator's
terminal. Houston stays framework independent; it adds no HTTP exec endpoint
and does not capture command output in deployment logs. The Houston CLI change
is implemented locally and not released yet; older CLIs still require an
authorized terminal inside the deployed app container.

Evidence (2026-10-12):

- Full `go test ./...` and `go vet ./...` passed in the Gantry toolchain with
  `GANTRY_TEST_POSTGRES_URL` pointing at a disposable PostgreSQL 17 container.
- `CGO_ENABLED=1 go test -race ./auth` passed with SQLite and PostgreSQL cases.
- Generator goldens refreshed; fully wired password and email-code apps passed
  their tests and Strict checks on both engines.
- Final ordinary-account creation lock optimization passed auth tests, vet and
  race checks. `TestCompletedSetupDoesNotLockUsersTable` confirms unrelated
  PostgreSQL user writes do not block creation once setup is complete.
- A separate generated app at `http://setup-preview.localhost/login` passed its
  tests and Strict (zero violations/advice). The real operator task returned a
  valid setup code without its value being displayed in tool output.
- Desktop and 375px phone layouts inspected; no horizontal overflow. Screenshots
  saved in `/private/tmp/gantry-otp-browser/screens/05-first-account-desktop.png`
  and `06-first-account-mobile.png`.
- No release, production deployment or Menu Manager change was made.
