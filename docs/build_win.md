# Building and installing (Windows)

English | [日本語](build_win.ja.md)

The macOS instructions are in [build_mac.md](build_mac.md). How the Windows version works is described in [chapter 10 of encryption.md](encryption.md#10-how-it-works-on-windows).

On Windows, se-env consists of four files:

| File | Language | Source | Role |
|---|---|---|---|
| `se-env.exe` | Go | `cmd/se-env` | the program, built from the same source as on macOS |
| `se_env_win.dll` | Rust | `win/se-env-win` | the Windows API calls se-env makes (ACLs, file locks, process hardening, job objects, socket peers, in-memory encryption); loaded by se-env.exe without cgo |
| `age-plugin-winhello.exe` | Rust | `win/age-plugin-winhello` | age plugin for the Windows Hello key (CNG) |
| `se-env-ui.exe` | Rust | `win/se-env-ui` | dialogs (hidden input, confirmation, tampering alert) |

The three Rust programs share the crates `win/sewin` (Windows API) and `win/winhello-core` (key derivation and formats; no Windows dependency).

Run the commands below in PowerShell, one at a time, from the top directory of the repository.

## Requirements

- Windows 10 / 11 (x64) with TPM 2.0
- a user with Windows Hello set up (PIN, fingerprint or face)
- git, Go 1.26 or later
- Rust (stable, `x86_64-pc-windows-msvc`) and the Visual Studio Build Tools "Desktop development with C++". With winget:

```powershell
winget install --id Microsoft.VisualStudio.BuildTools --override "--quiet --wait --norestart --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended"
winget install --id Rustlang.Rustup
rustup default stable-x86_64-pc-windows-msvc
```

- for the tests, `sh` from Git for Windows (`C:\Program Files\Git\usr\bin\sh.exe`): the session tests use shell scripts.

## 1. Build the Rust parts

```powershell
cd win
cargo build --release --locked
cargo test --release --locked --workspace
cd ..
```

- `--locked`: use only the crate versions and hashes in `Cargo.lock`, and never rewrite it
- The C runtime is linked statically (`win/.cargo/config.toml`), so the files need no DLL besides Windows itself. This is required: once started, se-env lets the process load only DLLs signed by Microsoft
- The same file makes the programs load the DLLs they import from System32 only (`/DEPENDENTLOADFLAG:0x800`) and turns Control Flow Guard on. After a build, `dumpbin /loadconfig` shows `0800` as the `Dependent Load Flag`
- `cargo test` also checks that the process hardening (ASLR and the other mitigation policies) really takes effect
- The `winhello-core` tests also run on macOS and Linux (`cargo test -p winhello-core`)

## 2. Build se-env

```powershell
$env:GOTOOLCHAIN = "local"; $env:GOFLAGS = "-mod=readonly"
go mod download; go mod verify
go vet ./...
$env:SE_ENV_TEST_DLL_DIR = "$PWD\win\target\release"; go test ./...
New-Item -ItemType Directory -Force bin | Out-Null
go build -trimpath -o bin\se-env.exe .\cmd\se-env
```

- `SE_ENV_TEST_DLL_DIR`: where the tests find `se_env_win.dll`. They copy it to a new directory in `%TEMP%` first, because se-env refuses a DLL that others may write to (non-system drives often let Authenticated Users modify).
- The tests use software keys, so no Windows Hello prompt appears. The program itself refuses such keys.

## 3. Install

```powershell
$h = "$env:LOCALAPPDATA\se-env"
New-Item -ItemType Directory -Force "$h\bin" | Out-Null
Copy-Item bin\se-env.exe, win\target\release\se_env_win.dll, win\target\release\age-plugin-winhello.exe, win\target\release\se-env-ui.exe "$h\bin\"
# put <home>\bin on PATH (user environment)
[Environment]::SetEnvironmentVariable("Path", "$h\bin;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

Then, in a new terminal:

```powershell
se-env help       # check the paths under "Files"
se-env init       # create the Windows Hello key (once; Windows Hello asks twice)
```

- The home is always the directory above `se-env.exe`. Under `%LOCALAPPDATA%` its ACL already allows only you, SYSTEM and Administrators.
- se-env checks that the home, `bin`, the four files, `identity.txt`, `vault` and `run` are owned by you (or SYSTEM, Administrators) and that nobody else may write to them, and stops otherwise. `init` gives `vault` an ACL that does not inherit.
- Keep `se_env_win.dll` in the same directory as `se-env.exe`; se-env looks nowhere else.

## Updating

- Rust crates: `cargo update -p <crate>` changes `Cargo.lock`. Review the changes (especially `age-core`, `age-plugin`, `windows`, `windows-sys`) before rebuilding.
- When a function of `win/se-env-win` changes, raise `ABI_VERSION` there and `dllABI` in `cmd/se-env/win_windows.go` together; se-env does not use a DLL whose version differs.

Rebuilding replaces only the programs, never the key or the vault. The Windows Hello key lives in your Windows user; `identity.txt` refers to its name and public key.
