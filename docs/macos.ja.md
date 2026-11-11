# macOS 版

[English](macos.md) | 日本語

macOS で se-env をビルドしてインストールする手順と、macOS に固有の仕組みを説明します。両方の OS に共通する仕組みは [design.ja.md](design.ja.md) にあります。Windows 版は [windows.ja.md](windows.ja.md) です。

macOS 版では鍵を **Secure Enclave** に置き、復号のたびに **Touch ID**（使えないときはログインパスワード）を求めます。

## 1. 必要なもの

- Secure Enclave を持つ Mac（Apple シリコン、または T2 チップ搭載の Intel Mac）。Touch ID があると便利ですが、なければログインパスワードを使います。
- ビルドには git、Go 1.26 以降、Swift 6.2 以降（Xcode または Command Line Tools）が必要です。
- zsh（Tab 補完を使う場合。任意）

## 2. 構成

macOS 版は2つのプログラムでできています。

| プログラム | 言語 | ソース | 役割 |
|---|---|---|---|
| `se-env` | Go | このリポジトリ（`cmd/se-env`） | 本体。[age](https://github.com/FiloSottile/age) のライブラリと、`go.mod` に書かれたほかの Go モジュールを組み込んでいる |
| `age-plugin-se` | Swift | [remko/age-plugin-se](https://github.com/remko/age-plugin-se) | Secure Enclave の鍵を扱う。se-env が age のプラグインの仕組みで起動する |

## 3. ビルド

ソースを取得し、リポジトリの一番上のディレクトリで下のコマンドを1つずつ実行します。コマンドは数個だけなので、読んでから実行できます。

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

- `GOTOOLCHAIN=local`：別のバージョンの Go を自動でダウンロードしない
- `GOFLAGS=-mod=readonly`：`go.mod` と `go.sum` を書き換えない
- `go mod download` と `go mod verify`：age を含むすべてのモジュールを `go.sum` のハッシュと照合する
- `go test`：単体テストと翻訳のチェック。Secure Enclave は要りません（テストはソフトウェアの鍵を使います。本体はこの鍵を受け付けません）。セッションを試すために、短時間だけ se-env のプロセスをいくつか起動します

### age-plugin-se

プラグインは決められたコミットを**新しい空のディレクトリ**に取得し、このリポジトリの `patches/age-plugin-se-reason.patch` を当ててからビルドします。以前のチェックアウトを使い回さないでください。`swiftc` は `Sources/` にある `.swift` ファイルをすべてコンパイルするので、残っていたファイルや書き換えられたファイルがあると、コミットハッシュが一致していてもバイナリに入ってしまいます。

```sh
SE_COMMIT=a229872979aa1b46f72fd76106e39e2a097c2f1d      # タグ v0.2.1
OUT=$PWD/bin/age-plugin-se
SRC=$(mktemp -d)
git clone -q https://github.com/remko/age-plugin-se.git "$SRC"
git -C "$SRC" -c advice.detachedHead=false checkout -q "$SE_COMMIT"

# 2行とも OK と表示されること。どちらかが出なければ、ここでやめる。
test "$(git -C "$SRC" rev-parse HEAD)" = "$SE_COMMIT"   && echo "commit OK"
test -z "$(git -C "$SRC" status --porcelain --ignored)" && echo "tree OK (nothing but the commit's files)"

# Touch ID のダイアログに要求元・対象・操作を出すパッチ。変わるのが Crypto.swift だけであること。
git -C "$SRC" apply "$PWD/patches/age-plugin-se-reason.patch"
test "$(git -C "$SRC" status --porcelain --ignored)" = " M Sources/Crypto.swift" && echo "patch OK"

( cd "$SRC" && swiftc -O -parse-as-library -module-name age_plugin_se Sources/*.swift -o "$OUT" )
rm -rf "$SRC"
```

パッチは、se-env が環境変数 `SE_ENV_AUTH_REASON` で渡す文を、Touch ID のダイアログに出すようにします（`LAContext.localizedReason`）。それ以外の動作は変えません（7章）。

プラグインは `swiftc` でビルドし、Apple のフレームワーク（CryptoKit と LocalAuthentication）にだけ依存します。`'21.0' is not a valid version number for macOS` のような警告は上流のコードによるもので、任意の耐量子鍵（`--pq`）にしか関係しません。ここでは無視して構いません。

### パッケージマネージャのプラグインを使う場合

age-plugin-se は主なパッケージマネージャでも入手できます。

```sh
brew install age-plugin-se          # Homebrew
sudo port install age-plugin-se     # MacPorts
nix profile install nixpkgs#age-plugin-se
```

se-env はプラグインを、まず `<ホーム>/bin` で探し、なければ `PATH` の絶対パスのディレクトリから探します。実際にどれが使われるかは `se-env help` の「プラグイン」で確認できます。この場合は、パッケージのビルド工程を信頼することになり、上で固定しているバージョンとは違うものが入る場合があります。パッケージのプラグインにはパッチが当たっていないので、Touch ID のダイアログには要求元・対象・操作が出ず、macOS の既定の文だけになります。プラグインやそのディレクトリにグループや他人が書き込める場合、se-env は動作を止めます。一部の Intel Mac の `/usr/local/bin` がこれに当たります。その場合は、プラグインを `<ホーム>/bin` にコピーしてください。

## 4. インストール

```sh
HOME_DIR=~/.local/share/se-env      # ホーム。macOS のプライバシー保護の対象フォルダにも、iCloud Drive にも入らない
mkdir -p "$HOME_DIR/bin" ~/.local/bin
chmod 700 "$HOME_DIR"
cp -p  bin/se-env "$HOME_DIR/bin/"
[ -x bin/age-plugin-se ] && cp -p bin/age-plugin-se "$HOME_DIR/bin/"   # PATH のものを使う場合は不要
cp -Rp completions "$HOME_DIR/"
ln -sf "$HOME_DIR/bin/se-env" ~/.local/bin/se-env    # ~/.local/bin を PATH に入れておく
se-env help        # 「ファイル」のパスを確認する
se-env init        # Secure Enclave の鍵を作る（初回のインストールのときだけ）
```

- ホームは常に、シンボリックリンクを解決した「`bin/se-env` の1つ上のディレクトリ」です。後でホームを移動しても、張り直すのはリンクだけで、設定の変更は要りません。
- ホームのパスは77文字以内にしてください。セッションはホームの中にソケットを作り、macOS ではソケットのパスが104バイトまでに制限されているためです。それより長いと、se-env はセッションを始めません。

### シェルの設定（任意）

Tab 補完を使うには、`~/.zshrc` の compinit より**前**（oh-my-zsh なら `source $ZSH/oh-my-zsh.sh` より前）に次を追加します。

```sh
fpath=("${${:-$HOME/.local/bin/se-env}:A:h:h}/completions" $fpath)
```

メッセージをいつも日本語にするには、`~/.zshrc` に次の1行を追加します。`LANG` などがすでに `ja_JP.UTF-8` のような日本語の値なら、追加しなくても日本語になります。

```sh
export SE_ENV_LANG=ja
```

Claude Code のようなツールが起動する se-env（MCP サーバーの `headersHelper` など）はそのツールの環境変数を受け継ぐので、ツールをこのシェルから起動していれば、この設定も効きます。

シェルのショートカットの例は README の「[ほかのツールとの組み合わせ例](../README.ja.md#ほかのツールとの組み合わせ例)」にあります。

## 5. 鍵（Secure Enclave）

`se-env init` は次のコマンドで鍵を作ります。

```
age-plugin-se keygen --access-control any-biometry-or-passcode
```

- Secure Enclave が P-256 の鍵共有用の鍵を作ります。アクセス制御のフラグは `privateKeyUsage` と `userPresence`、アクセス可能性は `WhenUnlockedThisDeviceOnly` です。
  - `userPresence` は、Touch ID、または生体認証が使えないときのログインパスワードによる本人確認を意味します。
  - `ThisDeviceOnly` は、鍵が他の機器に同期も移行もされないことを意味します。
- Secure Enclave は鍵を不透明な形で返し、それが `identity.txt`（`AGE-PLUGIN-SE-1…` の行）に保存されます。このデータは作成した Mac の Secure Enclave でしか使えません。
- 公開鍵は `age1se1…` の形で、`identity.txt` から求めます。

## 6. 暗号化と復号

age-plugin-se が作る宛先スタンザは次の形です。

```
宛先スタンザ "piv-p256":
    使い捨ての P-256 鍵ペア（eph）
    共有値   = ECDH(eph の秘密鍵, 宛先の公開鍵)
    包み鍵   = HKDF-SHA-256(共有値, salt = eph の公開鍵 ‖ 宛先の公開鍵, info = "piv-p256")
    本文     = ChaCha20-Poly1305(包み鍵, nonce = 0, ファイル鍵)
```

復号では、プラグインが Secure Enclave に鍵共有の計算を依頼します。

```
se-env（age ライブラリ）── "piv-p256" スタンザを読み、プラグインを起動する
             ▼
           age-plugin-se ──► Secure Enclave
                               • 本人確認を要求  ← Touch ID / パスワードの画面
                               • 共有値 = ECDH(SE の秘密鍵, eph の公開鍵)
             ◄── 包み鍵 ── HKDF-SHA-256(...) ── ファイル鍵を取り出す
```

`exec` は、全キーを環境変数に加えてから `exec` でコマンドに置き換わります。コマンドはその環境変数を引き継ぎます。

## 7. Touch ID のダイアログ

Touch ID のダイアログはプラグインが出すので、macOS が表示する名前は「age-plugin-se」だけです。パッチを当てたプラグイン（3章）は、se-env が渡した文をダイアログに出します（[design.ja.md](design.ja.md) の4章）。

```text
“age-plugin-se”で次の操作を実行しようとしています:
要求元: Ghostty › login › zsh › claude › zsh › se-env
対象: プロファイル「work」のすべてのキー
操作: 環境変数にしてコマンド「gh repo list」に渡す
許可後: claude (PID 4242) のセッションが 15:30 までこのプロファイル全体を保持し、…（セッションの中だけ）

実行するには認証してください
```

- 1行目の「“age-plugin-se”で次の操作を実行しようとしています:」は macOS が表示する文なので、macOS の言語設定に従います。
- 「要求元」は、アプリケーションの中のプログラムはバンドルの名前、それ以外は起動したときの名前で表示します。
- プラグインがこの文を使うのは、親プロセスがプラグインと同じディレクトリにある `se-env` のときだけです（`proc_pidpath` で実行ファイルの実際のパスを比べます）。プラグインを直接起動したプログラムには、macOS の既定のダイアログが出ます。

## 8. ファイルと権限

- すべてのファイルは `umask 077`（所有者だけが読み書きできる権限）で作ります。
- se-env は秘密情報を扱う前に、ホーム、`bin/`、本体、プラグイン、`identity.txt`、`vault/`、`run/` の所有者が自分（または root）で、グループや他人が書き込めないことを確かめます。そうでなければ動作を止めます。
- セッションのソケットは権限 0600、`run/` は 0700 です。
- ダイアログ（伏せ字の入力、確認、改ざんの警告）には常に `/usr/bin/osascript` を使い、`PATH` からは探しません。

## 9. セッションのメンバーの判定

エージェントは接続のたびに、相手のユーザーとプロセス ID をカーネルに尋ね（`LOCAL_PEERCRED`、`LOCAL_PEERPID`）、そのプロセスをカーネルのプロセス表で調べます。プロファイルを渡すのは、相手が次の条件をすべて満たすときだけです。

- 同じユーザーで動いている
- `<コマンド>` 自身か、その子孫である（親プロセスをたどると `<コマンド>` に行き着く）
- 接続を受け付けた時点より前に起動している（使い回されたプロセス ID は数えない）

親がすべて終了したプロセス（launchd の子に付け替えられたデーモン）は、セッションに属さなくなります。

## 10. macOS に固有の限界

[design.ja.md](design.ja.md) の7章に加えて、macOS では次の点があります。

- 動作中のコマンドの環境変数：macOS では、あるプロセスの環境変数を、同じユーザーの別のプロセスが `ps -wwE` で読めます。隠されるのは Apple のシステムのプログラムだけです。
- セッションに入り込む：macOS がデバッグを許している場合、デバッガでセッション内のプロセスを乗っ取ることができます。同じ能力があれば、エージェントのメモリも読めます。
- パスワードでの代用：`any-biometry-or-passcode` では、ログインパスワードでも本人確認が通ります。
- メモリ：se-env はコアダンプを無効にしています。
- 可用性：Mac を失ったり初期化したりすると、保管庫は二度と復号できません。

## 11. ビルドの完全性

3章のとおり、`go mod verify` ですべての Go モジュール（age のライブラリを含む）を `go.sum` に固定されたハッシュと照合し、`GOTOOLCHAIN=local` と `GOFLAGS=-mod=readonly` で se-env をビルドします。age-plugin-se は決められたコミットを新しい空のディレクトリに取得します。コミットハッシュが一致することと、**チェックアウトの中にそれ以外のファイルがないこと**を確かめ、パッチを当てて変わったのが `Sources/Crypto.swift` だけであることを確かめてから、`swiftc` でビルドします。関わるコードは se-env のバイナリとこのプラグインだけです。

## 12. 更新するとき

- Go モジュール：`go get <モジュール>@<バージョン>` と `go mod tidy` で `go.mod` と `go.sum` が更新されます。ビルドし直す前に、変更内容（特に `filippo.io/age`）を確かめてください。
- age-plugin-se：3章のコマンドのコミットハッシュを書き換えます。先に上流の変更内容を読んでください。特に、identity や宛先の形式に関する変更には注意が必要です。パッチが当たらなくなったら、`patches/age-plugin-se-reason.patch` を新しいコミットに合わせて作り直します。

ビルドし直して置き換わるのはバイナリだけで、鍵や保管庫には影響しません。新しいバージョンを使い始める前に、既存のプロファイルを `se-env get` で復号できることを確かめてください。
