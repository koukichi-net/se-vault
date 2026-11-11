# Building and installing

English | [日本語](build.ja.md)

se-env consists of two programs:

| Program | Language | Source | Role |
|---|---|---|---|
| `se-env` | Go | this repository (`cmd/se-env`) | the tool. It links the [age](https://github.com/FiloSottile/age) library and the other Go modules listed in `go.mod` |
| `age-plugin-se` | Swift | [remko/age-plugin-se](https://github.com/remko/age-plugin-se) | the Secure Enclave key. se-env runs it through age's plugin protocol |

There is no build script: the commands below are few, and you can read each one before running it. Run them from the repository root. Requirements: git, Go 1.26 or later, Swift 6.2 or later (Xcode or the Command Line Tools). The separate `age` and `age-keygen` programs are **not** needed.

## 1. Build se-env

```sh
export GOTOOLCHAIN=local GOFLAGS=-mod=readonly
go mod download && go mod verify
go vet ./... && go test ./...
mkdir -p bin
go build -trimpath -o bin/se-env ./cmd/se-env
```

- `GOTOOLCHAIN=local`: never download a different Go toolchain
- `GOFLAGS=-mod=readonly`: never modify `go.mod` or `go.sum`
- `go mod download` and `go mod verify`: check every module, age included, against the hashes in `go.sum`
- `go test`: unit tests and translation checks. They need no Secure Enclave (they use software keys, which the program itself refuses) and start a few short-lived se-env processes to test sessions

## 2. Build age-plugin-se

The plugin is built from a pinned commit in a **new, empty directory**, with `patches/age-plugin-se-reason.patch` from this repository applied. Do not reuse an old checkout: `swiftc` compiles every `.swift` file it finds in `Sources/`, so a leftover or modified file would end up in the binary even though the commit hash matches.

```sh
SE_COMMIT=a229872979aa1b46f72fd76106e39e2a097c2f1d      # tag v0.2.1
OUT=$PWD/bin/age-plugin-se
SRC=$(mktemp -d)
git clone -q https://github.com/remko/age-plugin-se.git "$SRC"
git -C "$SRC" -c advice.detachedHead=false checkout -q "$SE_COMMIT"

# Both lines must print OK. Stop if either does not.
test "$(git -C "$SRC" rev-parse HEAD)" = "$SE_COMMIT"   && echo "commit OK"
test -z "$(git -C "$SRC" status --porcelain --ignored)" && echo "tree OK (nothing but the commit's files)"

# The patch that shows who asks, on what and for what in the Touch ID dialog. Only Crypto.swift may change.
git -C "$SRC" apply "$PWD/patches/age-plugin-se-reason.patch"
test "$(git -C "$SRC" status --porcelain --ignored)" = " M Sources/Crypto.swift" && echo "patch OK"

( cd "$SRC" && swiftc -O -parse-as-library -module-name age_plugin_se Sources/*.swift -o "$OUT" )
rm -rf "$SRC"
```

The patch makes the plugin show the text se-env passes in the environment variable `SE_ENV_AUTH_REASON` in the Touch ID dialog (`LAContext.localizedReason`). The text is used only when the plugin's parent process is the `se-env` in the same directory as the plugin, so another program that starts the plugin itself cannot change the dialog. Nothing else changes; read `patches/age-plugin-se-reason.patch` to check.

The plugin is compiled with plain `swiftc`, not SwiftPM. SwiftPM would also fetch `swift-crypto`, which is used only on Linux and Windows. On macOS the plugin depends only on Apple frameworks (CryptoKit, LocalAuthentication). Warnings such as `'21.0' is not a valid version number for macOS` come from upstream code and affect only the optional post-quantum keys (`--pq`). They are harmless here.

### Alternative: the plugin from a package manager

age-plugin-se is packaged by common package managers:

```sh
brew install age-plugin-se          # Homebrew
sudo port install age-plugin-se     # MacPorts
nix profile install nixpkgs#age-plugin-se
```

se-env looks for the plugin in `<home>/bin` first and then in the absolute directories of your `PATH`; `se-env help` shows which copy is used, under "plugin". With this option you trust the package's build pipeline instead of your own build, and the version may differ from the one pinned above. A packaged plugin is not patched, so the Touch ID dialog shows only macOS's default text, without the caller, target and action. se-env refuses to run if the plugin or its directory is writable by group or others, which is the case for `/usr/local/bin` on some Intel Macs; copy the plugin to `<home>/bin` then.

## 3. Install

```sh
HOME_DIR=~/.local/share/se-env
mkdir -p "$HOME_DIR/bin" ~/.local/bin
chmod 700 "$HOME_DIR"
cp -p  bin/se-env "$HOME_DIR/bin/"
[ -x bin/age-plugin-se ] && cp -p bin/age-plugin-se "$HOME_DIR/bin/"   # unless it comes from PATH
cp -Rp completions "$HOME_DIR/"
ln -sf "$HOME_DIR/bin/se-env" ~/.local/bin/se-env    # ~/.local/bin must be on PATH
se-env help        # check the paths under "Files"
se-env init        # create the Secure Enclave key (first install only)
```

The home is always the directory above the program, so nothing else needs configuring. Everything in it must be owned by you and not writable by group or others; se-env stops otherwise.

Then add the Tab completion line from the README, and any of its shell examples, if you want them.

## Updating

- Go modules: `go get <module>@<version>` and `go mod tidy` update `go.mod` and `go.sum`. Review the changes (especially to `filippo.io/age`) before rebuilding.
- age-plugin-se: change the commit hash in the commands above. Read the upstream changes first, especially anything about the identity or recipient formats. If the patch no longer applies, redo `patches/age-plugin-se-reason.patch` against the new commit.

Rebuilding replaces only the binaries, never your key or vault. Before you rely on a new version, check that `se-env get` still decrypts an existing profile.