# How kc-env encrypts your secrets

English | [日本語](encryption.ja.md)

This document describes what kc-env stores, how it is encrypted and decrypted, how sessions work, and what is (and is not) protected. kc-env is a Go program. All cryptography is done by the [age](https://github.com/FiloSottile/age) library (format v1), linked into the binary, and by [age-plugin-se](https://github.com/remko/age-plugin-se), which talks to the Secure Enclave through Apple's CryptoKit. age runs the plugin as a separate process through its plugin protocol.

## 1. Components

| Piece | Role |
|---|---|
| Secure Enclave (SE) | Generates and holds the P-256 private key. The key never leaves the SE, and every use requires user presence. |
| age-plugin-se | age plugin that creates SE keys and asks the SE to perform key agreement. |
| age (library) | Encrypts and decrypts files: header with one stanza per recipient, plus an authenticated payload. |
| kc-env | Manages profiles, keeps values in memory, hands them to commands through environment variables, and runs sessions. |

## 2. Files

| File | Contents | Secret? |
|---|---|---|
| `identity.txt` | Exactly one `AGE-PLUGIN-SE-1…` line: an opaque blob for the SE key, plus comments (creation date, access control, public key). The blob can be used only by the Secure Enclave of the Mac that created it. | Not usable elsewhere, but **required**: without it the vault cannot be decrypted. |
| `vault/<profile>.age` | age-encrypted profile. | Encrypted |
| `vault/<profile>.keys` | Key names of the profile, one per line. Only a hint for listing. | **Plaintext** names, no values |
| `vault/.<profile>.lock` | Empty lock file, so two commands cannot change one profile at the same time. | No |
| `run/<id>.sock` | Unix socket of a running session (see §6). | No. Nothing secret is on disk |

There is no separate public-key file. The public key (`age1se1…`) is derived from `identity.txt` every time kc-env encrypts, by asking age-plugin-se; this needs no Touch ID. A `recipient.txt` left by kc-env 0.4 or earlier is ignored and can be deleted.

kc-env accepts nothing else in `identity.txt`: one line, and it must be a Secure Enclave key. A plain software key or a key of another plugin is refused, so a replaced file cannot quietly remove the Touch ID requirement.

Every file is created with `umask 077` (owner-only permissions). Writes go to a temporary file in the same directory followed by a rename, so a profile is never half-written. Before working with secrets, kc-env checks that the home, `bin/`, the program, the plugin, `identity.txt`, `vault/` and `run/` are owned by you (or root) and are not writable by group or others, and stops otherwise.

The home is always the directory above the program (`<home>/bin/kc-env`, resolved through symlinks). It cannot be changed through the environment.

## 3. Key generation (`kc-env init`)

```
age-plugin-se keygen --access-control any-biometry-or-passcode
```

- The SE generates a P-256 key-agreement key with the access control flags `privateKeyUsage` and `userPresence`, accessibility `WhenUnlockedThisDeviceOnly`.
  - `userPresence` means Touch ID, or the login password when biometrics are unavailable.
  - `ThisDeviceOnly` means the key is never synced or migrated to another device.
- The SE returns an opaque representation of the key, which is stored in `identity.txt`.

## 4. Encrypting a profile

A profile is serialized as one line per key:

```
API_TOKEN=<base64(value)>
OTHER_KEY=<base64(value)>
```

Base64 lets a value contain any characters, including `=` and newlines. The text is encrypted in memory by the age library. No value is ever written to disk in plaintext or put on a command line, where other processes could see it.

```
              plaintext profile
                     │
age:  file key = 16 random bytes
      ├─ header ─ recipient stanza "piv-p256" (built by age-plugin-se):
      │     ephemeral P-256 key pair (eph)
      │     shared  = ECDH(eph.private, recipient public key)
      │     wrapKey = HKDF-SHA-256(shared, salt = eph.public ‖ recipient.public, info = "piv-p256")
      │     body    = ChaCha20-Poly1305(wrapKey, nonce = 0, file key)
      │     header MAC = HMAC-SHA-256(HKDF(file key, "header"), header)
      └─ payload: nonce = 16 random bytes
            payloadKey = HKDF-SHA-256(file key, salt = nonce, info = "payload")
            STREAM of ChaCha20-Poly1305 chunks (64 KiB each)
                     │
             vault/<profile>.age
```

The recipient is always the single public key derived from `identity.txt`. Encryption uses only that **public** key, so it needs no Touch ID. This is why the first `set` into a new profile does not prompt.

### Key names

A profile is handed to commands as environment variables, so a key name must be a valid variable name. Names that change how a program is loaded or started are refused: `PATH`, `HOME`, `SHELL`, `IFS`, `ENV`, `BASH_ENV`, `ZDOTDIR`, `NODE_OPTIONS`, `PYTHONPATH`, `PYTHONSTARTUP`, `PERL5OPT`, `RUBYOPT`, `GIT_SSH_COMMAND`, `AGEDEBUG`, and everything starting with `DYLD_`, `LD_` or `KC_ENV_`. The same check runs when a profile is read: a profile that contains such a name, a malformed name, a duplicate name, or a value with a NUL byte is not handed to any command. `kc-env rm <profile> <KEY>` can still remove the offending key. The list is a safety net, not a complete catalogue of dangerous variables.

## 5. Decrypting a profile

```
kc-env (age library) ── decrypt vault/<profile>.age with identity.txt
             │  reads the "piv-p256" stanza and starts the plugin
             ▼
           age-plugin-se ──► Secure Enclave
                               • requires user presence  ← Touch ID / password prompt
                               • shared = ECDH(SE private key, eph.public)
             ◄── wrapKey ── HKDF-SHA-256(...) ── unwrap file key
           age: verifies the header MAC, decrypts and authenticates the payload
             │
kc-env ◄── plaintext (in memory) ── parsed into KEY/value pairs
```

What kc-env does with the result:

| Command | Use of the decrypted profile |
|---|---|
| `get` | prints one value |
| `exec` | adds every key to the environment, then `exec`s the command, which inherits them |
| `header` | prints `{"<name>":"<prefix><value>"}` (JSON-encoded) |
| `set` / `rm` on an existing profile | modifies the pairs and re-encrypts (§4) |
| `ls --verify` | rebuilds the key-name index |

`list`, `ls`, `profiles` and Tab completion read only file names and the `.keys` index. They never decrypt, so they never prompt. The index is not authenticated: it is used for display only, and a line that is not a key name makes kc-env ignore the whole index.

The plugin is started by name through `PATH`, so kc-env narrows its own `PATH` to the single directory that holds the plugin (`<home>/bin` first, then the absolute directories of your `PATH`). Commands started by `exec` and `session` get your original `PATH` back. kc-env also removes `AGEDEBUG` from its environment, because with `AGEDEBUG=plugin` the age library would print the plugin protocol, file key included, to stderr. The dialog helper is always `/usr/bin/osascript`, never looked up on `PATH`.

## 6. Sessions

`kc-env session [--only p1,p2] [--ttl 30m] -- <command>` lets the selected profiles skip Touch ID after the first use.

The `kc-env session` process stays alive while `<command>` runs and acts as an **agent**, in the same way as ssh-agent:

1. Decrypted profiles are kept **only in the agent's memory**. Nothing is written to disk, and there is no session key. `KC_ENV_SESSION` holds only the session ID (8 random bytes), which tells kc-env which socket to use. It is not a secret.
2. The agent listens on a Unix socket, `run/<id>.sock` (mode 0600, in a 0700 directory). macOS limits socket paths to 104 bytes, so the home must be at most 77 characters long; otherwise kc-env refuses to start a session. For every connection it asks the kernel who connected (`LOCAL_PEERCRED`, `LOCAL_PEERPID`) and looks the process up in the kernel's process table. It hands out or accepts profiles only if the peer
   - runs as the same user, and
   - is `<command>` or a descendant of it (the chain of parent processes leads to `<command>`), and
   - was started before the connection was accepted (so a recycled process ID does not count).

   Setting `KC_ENV_SESSION` in some other process therefore achieves nothing. A process whose ancestors have all exited (a daemon re-parented to launchd) no longer belongs to the session.

   kc-env checks the other direction too. It reads a profile from a socket, or hands one to it, only if the process listening there runs as the same user and is one of kc-env's own ancestors, which the agent of its session always is. A look-alike socket put in `run/` by another program is not trusted.
3. On the first decryption of an allowed profile, kc-env:
   - asks the agent, which has nothing yet and gives the caller the per-profile lock. Other callers wait, so several commands starting at once share a single Touch ID. The lock is tied to the connection and is released when the caller exits for any reason.
   - decrypts with the SE (§5), which prompts
   - hands the plaintext to the agent, together with the SHA-256 of the `vault/<profile>.age` it decrypted
4. Later requests are answered from memory. Each time, the agent hashes the current `vault/<profile>.age`. If the file changed, the entry is wiped and the SE is used again. `set` through kc-env refreshes an entry that already exists; it never adds a profile that has not been decrypted in this session.
5. The lifetime and the scope are held by the agent and cannot be changed after the session starts:
   - `--ttl` (default 30 minutes, at most 24 hours) counts wall-clock time, including time the Mac sleeps. When it passes, the agent wipes its memory and removes the socket.
   - `--only` is checked by the agent for every request. Other profiles ask for Touch ID every time.
6. The cache also ends when
   - `<command>` exits (the agent wipes its memory and exits),
   - `kc-env lock` is run (the cache is dropped; the session continues),
   - `kc-env cache rm` is run (after a Touch ID check, the session ends; the command keeps running and asks for Touch ID from then on),
   - the agent is killed (its memory is gone; the leftover socket is removed the next time kc-env sees it).

`kc-env cache list` works from outside a session: any process of the same user may ask an agent for its metadata (ID, command, start, expiry, scope, names of cached profiles), and may tell it to drop its cache. Neither reveals a value; dropping a cache only makes kc-env ask for Touch ID again.

## 7. Presence checks for deleting

Deleting never relies on a confirmation alone, and never on a session. Each of these needs the Secure Enclave key, and therefore Touch ID, even inside a session:

| Command | Check |
|---|---|
| `rm <profile> <KEY>…` | decrypts the profile with the SE key (the session cache is not used) |
| `rm --all`, `rm-profile --force` | asks `y/N` (or a dialog), then the presence check below |
| `rm-profile` | asks, then decrypts the profile with the SE key to see that it really is empty (the index is only a hint) |
| `cache rm` | the presence check below |
| `destroy` | `y/N`, a typed phrase, a 10-second countdown, then the presence check below |

The presence check encrypts a random 24-byte challenge to the public key derived from `identity.txt` and decrypts it with the SE identity, which always produces a Touch ID prompt, then compares the result.

`destroy` also refuses to run without an interactive terminal or inside a session, and tells every running session to end before it deletes anything.

These checks stop kc-env itself from being used to delete secrets unattended (for example by a tool running commands for you). They are not a barrier against a program that deletes the files directly: anything running as you can do that.

## 8. Threat model

### Protected

- Secrets at rest: no plaintext is written to disk, sessions included. The vault, and any backup or copy of it (Time Machine, disk images, a stolen drive), cannot be decrypted without this Mac's Secure Enclave and your presence.
- Other Macs and other users: the SE key is device-bound, the files are owner-only, and kc-env stops if they are not.
- Silent use outside a session: a process running as you can start kc-env, but every decryption shows a system prompt that you can decline.
- Processes outside a session: a session's cache is in the agent's memory and is given only to processes started under the session's command. There is no key in the environment and no cache file, so reading another process's environment (`ps -wwE`) or the files under `run/` yields nothing.
- Session limits: the TTL and `--only` live in the agent. A process inside the session cannot extend or widen them.
- Tampering with ciphertext: age authenticates the header and payload. A modified file fails to decrypt.
- A changed public key: profiles are encrypted only to the key derived from `identity.txt`. No file can add a second recipient.
- Command lines: values are never put in a command's arguments, which every user of the Mac can see with `ps`. They are kept in memory and passed to commands only through environment variables.

### Not protected, or limits

- Inside a session, every process started under the session's command can read the cached profiles without a prompt, and can change them (`set`). That is what a session is for; it includes any tool or agent you start there, and anything that such a program runs. Keep `--only` narrow and the TTL short, and use `kc-env lock` or `cache rm` when you are done.
- Getting into a session's process tree: a program running as you cannot simply claim membership, but it can try to be started by a member (for example by changing a script or a configuration file that a program in the session runs), or to take over a member with a debugger if macOS allows it to debug your processes. The same ability would let it read the agent's memory. A session narrows who can read the cache; it is not a sandbox.
- After hand-off: once a value is exported to a command, that command and anything it starts can read it, log it, or send it elsewhere.
- The environment of a running command: on macOS, other processes of the same user can read a process's environment (`ps -wwE`); only Apple's own system programs are hidden. A value that `exec` puts in a command's environment can therefore be read by anything running as you, without a prompt, for as long as that command runs. A session does not change this: it keeps the session's cache out of the environment, not the values that `exec` hands to a command. Keep commands that hold secrets short-lived where you can. `header` hands a value over through standard output instead of the environment.
- Secrets exported somewhere else: kc-env cannot protect a copy of a secret that is still exported in a shell startup file, or set in a parent process. Such a value is in the environment of every program you start. After moving a secret into kc-env, remove the old `export`, then reissue the secret and restart your terminals and long-running programs: a running process keeps the environment it was started with, even after the startup file is fixed.
- Malware running as you can wait for you to approve a prompt it triggered, or trick you into approving one. It can also replace the kc-env program, the plugin or `identity.txt` (all owned by you), and delete the vault. The prompt is a speed bump, not an isolation boundary.
- There is no sender authentication: age proves a file was not modified, but not who created it. Anyone who can write to `vault/` could replace a profile with content encrypted to your public key, and kc-env would accept it, apart from the key names it refuses (§4). Keep the home directory writable only by you.
- Key names are plaintext (`.keys`), so they can be listed without a prompt.
- Password fallback: with `any-biometry-or-passcode`, the login password also satisfies the presence check.
- Memory: Go cannot guarantee that every copy of a value is erased. kc-env overwrites the session cache when it is dropped and disables core dumps, but values may remain in freed memory until the process exits.
- Availability: losing or erasing the Mac makes the vault unrecoverable. There is no recovery key, by design. Be able to reissue your secrets.

## 9. Build integrity

kc-env ships no build script. [build.md](build.md) lists the few commands to run by hand: `go mod verify` checks every Go module (the age library included) against the hashes pinned in `go.sum`, and kc-env is built with `GOTOOLCHAIN=local` (so no toolchain is downloaded) and `GOFLAGS=-mod=readonly`. age-plugin-se is cloned into a new, empty directory at a pinned commit; the commit hash **and the absence of any other file in the checkout** are verified before it is compiled directly with `swiftc`, which needs no package manager. The kc-env binary and the plugin are the only code involved.
