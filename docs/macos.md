# macOS

English | [日本語](macos.ja.md)

How to build and install se-env on macOS, and what is specific to macOS. What both OSes share is in [design.md](design.md). The Windows version is in [windows.md](windows.md).

On macOS the key lives in the **Secure Enclave**, and every decryption asks for **Touch ID** (or your login password when it is unavailable).

## 1. Requirements

- A Mac with a Secure Enclave (Apple silicon, or an Intel Mac with a T2 chip). Touch ID is recommended; without it, your login password is used.
- To build: git, Go 1.26 or later, and Swift 6.2 or later (Xcode or the Command Line Tools).
- zsh, for Tab completion (optional).

## 2. Components

The macOS version consists of two programs:

| Program | Language | Source | Role |
|---|---|---|---|
| `se-env` | Go | this repository (`cmd/se-env`) | the tool. It links the [age](https://github.com/FiloSottile/age) library and the other Go modules listed in `go.mod` |
| `age-plugin-se` | Swift | [remko/age-plugin-se](https://github.com/remko/age-plugin-se) | the Secure Enclave key. se-env runs it through age's plugin protocol |

## 3. Build

Get the source and run the commands below from the repository root. They are few, and you can read each one before running it.

```sh
git clone https://github.com/koukichi-net/se-vault.git
cd se-vault
```

### se-env

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

### age-plugin-se

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

The patch makes the plugin show the text se-env passes in the environment variable `SE_ENV_AUTH_REASON` in the Touch ID dialog (`LAContext.localizedReason`). Nothing else changes (§7).

The plugin is compiled with `swiftc` and depends only on Apple frameworks (CryptoKit, LocalAuthentication). Warnings such as `'21.0' is not a valid version number for macOS` come from upstream code and affect only the optional post-quantum keys (`--pq`). They are harmless here.

### Using a packaged plugin

age-plugin-se is packaged by common package managers:

```sh
brew install age-plugin-se          # Homebrew
sudo port install age-plugin-se     # MacPorts
nix profile install nixpkgs#age-plugin-se
```

se-env looks for the plugin in `<home>/bin` first and then in the absolute directories of your `PATH`; `se-env help` shows which copy is used, under "plugin". You then trust the package's build pipeline, and the version may differ from the one pinned above. A packaged plugin is not patched, so the Touch ID dialog shows only macOS's default text, without the caller, target and action. se-env refuses to run if the plugin or its directory is writable by group or others, which is the case for `/usr/local/bin` on some Intel Macs; copy the plugin to `<home>/bin` then.

## 4. Install

```sh
HOME_DIR=~/.local/share/se-env      # the home: outside the folders covered by macOS privacy prompts and outside iCloud Drive
mkdir -p "$HOME_DIR/bin" ~/.local/bin
chmod 700 "$HOME_DIR"
cp -p  bin/se-env "$HOME_DIR/bin/"
[ -x bin/age-plugin-se ] && cp -p bin/age-plugin-se "$HOME_DIR/bin/"   # unless it comes from PATH
cp -Rp completions "$HOME_DIR/"
ln -sf "$HOME_DIR/bin/se-env" ~/.local/bin/se-env    # ~/.local/bin must be on PATH
se-env help        # check the paths under "Files"
se-env init        # create the Secure Enclave key (first install only)
```

- The home is always the directory above `bin/se-env`, with symlinks resolved. If you move it later, only the symlink needs updating.
- Keep the home path at 77 characters or fewer. Sessions create a socket inside it, and macOS limits socket paths to 104 bytes. With a longer path, se-env refuses to start a session.

### Shell setup (optional)

For Tab completion, add this to `~/.zshrc` **before** compinit runs (with oh-my-zsh, before `source $ZSH/oh-my-zsh.sh`):

```sh
fpath=("${${:-$HOME/.local/bin/se-env}:A:h:h}/completions" $fpath)
```

To always use Japanese, add this line to `~/.zshrc`. It is not needed if `LANG` or the like is already Japanese, such as `ja_JP.UTF-8`.

```sh
export SE_ENV_LANG=ja
```

se-env started by a tool such as Claude Code (for example as an MCP server's `headersHelper`) inherits that tool's environment, so the setting applies when you start the tool from this shell.

Examples of shell shortcuts are in "[Using se-env with other tools](../README.md#using-se-env-with-other-tools-examples)" in the README.

## 5. The key (Secure Enclave)

`se-env init` creates the key with:

```
age-plugin-se keygen --access-control any-biometry-or-passcode
```

- The Secure Enclave generates a P-256 key-agreement key with the access control flags `privateKeyUsage` and `userPresence`, accessibility `WhenUnlockedThisDeviceOnly`.
  - `userPresence` means Touch ID, or the login password when biometrics are unavailable.
  - `ThisDeviceOnly` means the key is never synced or migrated to another device.
- The Secure Enclave returns an opaque representation of the key, which is stored in `identity.txt` (an `AGE-PLUGIN-SE-1…` line). It can be used only by the Secure Enclave of the Mac that created it.
- The public key, `age1se1…`, is derived from `identity.txt`.

## 6. Encryption and decryption

age-plugin-se writes this recipient stanza:

```
recipient stanza "piv-p256":
    ephemeral P-256 key pair (eph)
    shared   = ECDH(eph private, recipient public)
    wrap key = HKDF-SHA-256(shared, salt = eph public ‖ recipient public, info = "piv-p256")
    body     = ChaCha20-Poly1305(wrap key, nonce = 0, file key)
```

To decrypt, the plugin asks the Secure Enclave for the key agreement:

```
se-env (age library) ── reads the "piv-p256" stanza and starts the plugin
             ▼
           age-plugin-se ──► Secure Enclave
                               • asks for user presence  ← Touch ID / password prompt
                               • shared = ECDH(SE private key, eph public)
             ◄── wrap key ── HKDF-SHA-256(...) ── recover the file key
```

`exec` adds every key to the environment and then replaces itself with the command (`exec`), which inherits the environment.

## 7. The Touch ID dialog

The Touch ID dialog is shown by the plugin, so macOS names only "age-plugin-se". A patched plugin (§3) adds the text se-env passes ([design.md](design.md) §4):

```text
“age-plugin-se” is trying to use the se-env key:
Requested by: Ghostty › login › zsh › claude › zsh › se-env
Target: every key in profile 'work'
Action: pass them as environment variables to the command "gh repo list"
After approval: the session of claude (PID 4242) keeps this whole profile until 15:30, ...   (in a session only)

Authenticate only if you expected this request.
```

- The first line ("“age-plugin-se” is trying to …") is written by macOS and follows the macOS language setting.
- "Requested by" shows the application bundle name for a program inside one, otherwise the name the program was started as.
- The plugin uses the text only when its parent process is the `se-env` in its own directory (it compares the executables' real paths with `proc_pidpath`). A program that starts the plugin directly gets macOS's default dialog.

## 8. Files and permissions

- Every file is created with `umask 077` (readable and writable by the owner only).
- Before handling a secret, se-env checks that the home, `bin/`, the program, the plugin, `identity.txt`, `vault/` and `run/` are owned by you (or root) and not writable by group or others, and stops otherwise.
- Session sockets have mode 0600, and `run/` has mode 0700.
- Dialogs (hidden input, confirmation, the tampering alert) always use `/usr/bin/osascript`, never looked up on `PATH`.

## 9. Recognising session members

On every connection the agent asks the kernel for the peer's user and process ID (`LOCAL_PEERCRED`, `LOCAL_PEERPID`) and looks the process up in the kernel's process table. It hands a profile over only if the peer:

- runs as the same user,
- is `<command>` or a descendant of it (walking up its parents reaches `<command>`), and
- was started before the connection was accepted (a reused process ID does not count).

A process whose ancestors have all exited (a daemon re-parented to launchd) no longer belongs to the session.

## 10. Limits specific to macOS

In addition to [design.md](design.md) §7:

- The environment of a running command: on macOS, other processes of the same user can read a process's environment with `ps -wwE`; only Apple's own system programs are hidden.
- Getting into a session: if macOS allows debugging, a debugger can take over a process in the session. The same ability can read the agent's memory.
- Password fallback: with `any-biometry-or-passcode`, the login password also satisfies user presence.
- Memory: se-env disables core dumps.
- Availability: if you lose or erase the Mac, the vault can never be decrypted again.

## 11. Build integrity

As in §3, `go mod verify` checks every Go module (the age library included) against the hashes pinned in `go.sum`, and se-env is built with `GOTOOLCHAIN=local` and `GOFLAGS=-mod=readonly`. age-plugin-se is cloned into a new, empty directory at a pinned commit; the commit hash **and the absence of any other file in the checkout** are verified, the patch is applied and checked to change only `Sources/Crypto.swift`, and it is compiled with `swiftc`. The se-env binary and the plugin are the only code involved.

## 12. Updating

- Go modules: `go get <module>@<version>` and `go mod tidy` update `go.mod` and `go.sum`. Review the changes (especially to `filippo.io/age`) before rebuilding.
- age-plugin-se: change the commit hash in the commands of §3. Read the upstream changes first, especially anything about the identity or recipient formats. If the patch no longer applies, redo `patches/age-plugin-se-reason.patch` against the new commit.

Rebuilding replaces only the binaries, never your key or vault. Before you rely on a new version, check that `se-env get` still decrypts an existing profile.
