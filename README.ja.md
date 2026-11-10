# kc-env

[English](README.md) | 日本語

APIキーやトークンを、シェルの設定ファイルや `.env` に書かずに管理するためのツールです。秘密情報を**プロファイル**ごとに保管し、Mac の **Secure Enclave** の中にある鍵で暗号化します。値を復号するたびに **Touch ID**（使えないときはログインパスワード）を求めます。ネットワークは一切使いません。

```sh
kc-env set work GITHUB_TOKEN              # 値は非表示で入力し、暗号化して保存
kc-env set personal GITHUB_TOKEN          # 同じキー名でも、プロファイルごとに別の値
kc-env exec work -- gh repo list          # Touch ID 1回。値は gh にだけ渡る
```

## 特長

- 同じ変数名（例：`GITHUB_TOKEN`）に、プロファイルごとに別の値を持たせられます。
- 復号用の鍵はハードウェアに縛られています。鍵は Secure Enclave の中で作られ、外に取り出せません。保管庫をコピーしても、他の Mac では復号できません。
- 復号のたびに Touch ID を求めます。これは Secure Enclave 自身が強制します（パスワードでの代用あり）。
- 平文をディスクに書きません。値はメモリ上だけで扱い、実行するコマンドには環境変数としてだけ渡します。
- セッション（任意）では指定したプロファイルを一定時間メモリに保持します。まとまった作業でも Touch ID は1回で済みます。使えるのは、セッションの中で起動したプログラムだけです。
- 削除には、セッションの中でも必ず Touch ID を求めます。
- どのツールとも組み合わせられます。`exec` は秘密情報を環境変数として渡し、`header` は HTTP ヘッダーを JSON で出力します。特定のツール向けの連携機能はありません。AI のコーディング支援ツールを含め、ほかのツールとの組み合わせ方は[使用例](#ほかのツールとの組み合わせ例)で紹介しています。
- 英語と日本語に対応しています。`KC_ENV_LANG`・`LC_ALL`・`LC_MESSAGES`・`LANG` から言語を選びます。
- オフラインで動く小さな構成です。[age](https://github.com/FiloSottile/age) をライブラリとして組み込んだ Go のバイナリ1つと、Secure Enclave 用の [age-plugin-se](https://github.com/remko/age-plugin-se) だけで動きます。実行時に通信しません。

暗号化の仕組みは [docs/encryption.ja.md](docs/encryption.ja.md) で説明しています。

## 必要なもの

- Secure Enclave を持つ Mac（Apple シリコン、または T2 チップ搭載の Intel Mac）。Touch ID があると便利ですが、なければログインパスワードを使います。
- zsh（Tab 補完を使う場合。任意）
- ビルドには git、Go 1.26 以降、Swift 6.2 以降（Xcode または Command Line Tools）が必要です。

## インストール

ソースを取得します。

```sh
git clone https://github.com/koukichi-net/se-vault.git
cd se-vault
```

ビルド用のスクリプトはありません。[docs/build.ja.md](docs/build.ja.md) にある数個のコマンドで2つのプログラムをビルドします。kc-env はすべての Go モジュールを `go.sum` と照合してから `go build` で、age-plugin-se は決められたコミットを確かめてから `swiftc` でビルドします。そのあと、次のようにインストールします。

```sh
# 好きな場所（ホーム）に入れる。~/.local/share/kc-env がおすすめです。
# macOS のプライバシー保護の対象フォルダにも、iCloud Drive にも入らないためです。
mkdir -p ~/.local/share/kc-env ~/.local/bin
chmod 700 ~/.local/share/kc-env
cp -Rp bin completions ~/.local/share/kc-env/
ln -sf ~/.local/share/kc-env/bin/kc-env ~/.local/bin/kc-env   # ~/.local/bin を PATH に入れておく

kc-env init                   # Secure Enclave の鍵を作る（最初の1回だけ）
```

ホームは常に、シンボリックリンクを解決した「`bin/kc-env` の1つ上のディレクトリ」です。後でホームを移動しても、張り直すのはリンクだけで、設定の変更は要りません。環境変数でホームを変えることはできません。また、ホーム、プログラム、鍵のファイルに自分以外が書き込める場合、kc-env は動作を止めます。ホームのパスは77文字以内にしてください。セッションはホームの中に Unix ソケットを作り、macOS ではソケットのパスが104バイトまでに制限されているためです。

### シェルの設定（任意）

Tab 補完を使うには、`~/.zshrc` の compinit より**前**（oh-my-zsh なら `source $ZSH/oh-my-zsh.sh` より前）に次を追加します。

```sh
fpath=("${${:-$HOME/.local/bin/kc-env}:A:h:h}/completions" $fpath)
```

## 使い方

全コマンドと、実際に使われているパスは `kc-env help` で確認できます。

### 保存して使う

```sh
kc-env set work API_TOKEN                 # 非表示で入力。パイプも可: pbpaste | kc-env set work API_TOKEN
kc-env list                               # 全プロファイルとキー名（Touch ID 不要。値は表示しない）
kc-env ls work                            # 1つのプロファイルのキー名
kc-env exec work -- ./deploy.sh           # キーを環境変数にしてコマンドを実行
kc-env get work API_TOKEN                 # 値を1つ表示（出力が記録される場所では使わない）
kc-env header work API_TOKEN              # HTTP ヘッダー用の {"Authorization":"Bearer <値>"}
```

端末がない環境（TTY なしでコマンドを実行するツールなど）では、`set` は伏せ字の macOS ダイアログで値を尋ねます。

キー名は環境変数名として有効なものにしてください。プログラムの起動のしかたを変える名前（`PATH`、`HOME`、`DYLD_*`、`LD_*`、`NODE_OPTIONS` など）は使えません。プロファイル名には英数字と `.`、`_`、`-` が使えます。

### 削除

```sh
kc-env rm work API_TOKEN OTHER_KEY        # キーを削除
kc-env rm --all work                      # キーをすべて削除し、空のプロファイルは残す（先に確認あり）
kc-env rm-profile work                    # 空のプロファイルを削除。キーが残っていれば止まる
kc-env rm-profile --force work            # キーごとプロファイルを削除（先に確認あり）
```

どれも、セッションの中であっても Secure Enclave の鍵で Touch ID を求めます。セッションはプログラムに秘密情報を読ませるためのもので、無人で削除させるためのものではありません。

### セッション：しばらくの間 Touch ID を1回で済ませる

```sh
kc-env session --only work --ttl 30m -- <コマンド>   # 期限の既定は30分（最長24時間）。コマンドの既定はシェル
kc-env cache                              # このセッションの期限・対象・キャッシュ済みのプロファイル
kc-env cache list                         # この Mac で動いているすべてのセッション
kc-env cache rm --current | --all | <ID>  # セッションを終了する（Touch ID）
kc-env lock                               # このセッションのキャッシュを消す（次の利用で再び Touch ID）
```

セッションの中では、許可したプロファイルは最初の復号だけ Touch ID を求めます。そのあとは、セッションが終わるか期限が切れるまで、メモリから応答します。復号した内容は、ssh-agent と同じように `kc-env session` のプロセスが自分のメモリに持ちます。ディスクには何も書かず、環境変数にも鍵を入れません。応答するのは、`<コマンド>` の配下で起動したプロセスだけです。期限と `--only` もこのプロセスが管理するので、セッション内のプログラムが期限を延ばしたり対象を広げたりすることはできません。

**セッションの中で起動したプログラムは、どれも確認なしにキャッシュされた秘密情報を読めます**。そのプログラムが実行するものも同じです（[脅威モデル](docs/encryption.ja.md#8-脅威モデル)を参照）。`--only` の対象は絞り、期限は短くし、終わったら `kc-env lock` を実行してください。

### すべてを消す

```sh
kc-env destroy
```

Secure Enclave の鍵の参照と全プロファイルを削除し、すべてのセッションを終わらせます。プログラム本体は残るので、`kc-env init` からやり直せます。端末から直接、セッションの外にいるときだけ実行できます。失うものを表示してから、`y/N` の確認、確認用の文字列の入力、10秒の待機、Touch ID の順に進みます。**元に戻す方法はありません。**

### 言語

メッセージ、ヘルプ、Tab 補完の表示は、`KC_ENV_LANG`・`LC_ALL`・`LC_MESSAGES`・`LANG` のうち最初に値があるものに従います（`C` と `POSIX` は英語）。対応していない言語のときは英語になります。

```sh
KC_ENV_LANG=ja kc-env help
```

翻訳は [`cmd/kc-env/locales/`](cmd/kc-env/locales) に [go-i18n](https://github.com/nicksnyder/go-i18n) の TOML ファイルとして置いてあり、バイナリに埋め込まれます。言語を追加するには、`en.toml` を `<言語>.toml`（例：`de.toml`）としてコピーし、値を翻訳してから `go test ./...` を実行してください。メッセージの抜けや余分、存在しないプレースホルダーがあれば、テストが失敗して知らせます。複数形は go-i18n の CLDR の区分（`one`、`other` など）で書きます。

## ほかのツールとの組み合わせ例

kc-env 自体には、AI ツールを含め、特定のツールとの連携機能はありません。用意しているのは `exec`・`header`・`session` という汎用の部品で、以下はそれらを組み合わせる例です。自分の設定に合わせてコピーして使ってください。kc-env がインストールするものではありません。

### シェルのショートカット（`~/.zshrc`）

```sh
# プロファイルの秘密情報を渡してコマンドを実行する:   kx work terraform plan
kx() { local p=$1; shift; kc-env exec "$p" -- "$@"; }

# セッションでコマンドを起動し、Touch ID を1回で済ませる:   ks work zsh
ks() { local p=$1; shift; kc-env session --only "$p" -- "$@"; }

# 例: AI のコーディング支援ツール（ここでは Claude Code）を、work プロファイルの
# 30分のセッションで起動する。追加の引数はツールに渡る: ccs --resume
alias ccs='kc-env session --only work --ttl 30m -- claude'
```

セッションの中では、**そこで起動したプログラムは AI エージェントも含めて、あなたの確認なしにキャッシュされた秘密情報を使えます**。`--only` には作業に必要なプロファイルだけを指定し、期限は短めにしてください。

### 環境変数から秘密情報を読むツール

コマンドを `exec` で包みます。たとえば、AI ツールに設定する stdio 型の MCP サーバーなら次のとおりです。

```json
"my-server": {
  "command": "/Users/<you>/.local/bin/kc-env",
  "args": ["exec", "work", "--", "npx", "-y", "some-mcp-server"]
}
```

### コマンドの出力から HTTP ヘッダーを受け取るツール

`header` はヘッダーを1つ JSON で出力します。たとえば、Claude Code の HTTP 型 MCP サーバーの `headersHelper` なら次のとおりです。

```json
"my-server": {
  "type": "http",
  "url": "https://example.com/mcp",
  "headersHelper": "/Users/<you>/.local/bin/kc-env header work API_TOKEN"
}
```

ほかの形式のヘッダーには `--name` と `--prefix` を使います。例：`kc-env header work API_TOKEN --name X-Api-Key --prefix ''`

## セキュリティ上の注意

- Mac を失ったり初期化したりすると、保管庫は復号できなくなります。復旧用の鍵はなく、これは意図した仕様です。APIキーを再発行できる状態にしておいてください。
- キー名は Touch ID なしで一覧できるように平文で保存しています（`vault/<profile>.keys`）。値は常に暗号化されています。
- 値をコマンドに渡したあとは、そのコマンド（とそこから起動されたもの）が値を読めます。macOS では、そのコマンドが動いている間、あなたの権限で動く別のプロセスもその環境変数を読めます（`ps -wwE`）。セッションの中でも同じです。
- 秘密を kc-env に移したら、シェルの起動ファイルから古い `export` を削除し、秘密を再発行し、ターミナルを起動し直してください。動作中のプログラムは起動したときの環境変数を持ち続けます。
- あなたの権限で動くものは、kc-env 自身のファイルを差し替えたり、保管庫を消したりできます。Touch ID は無断の復号を止めますが、サンドボックスではありません。
- 脅威モデルと限界は [docs/encryption.ja.md](docs/encryption.ja.md) を見てください。

## ファイル構成

```
<ホーム>/
  bin/kc-env               本体（Go のバイナリ）
  bin/age-plugin-se        Secure Enclave 用プラグイン
  completions/_kc-env      zsh の補完
  identity.txt             Secure Enclave の鍵への参照（init で作成）
  vault/<profile>.age      暗号化したプロファイル
  vault/<profile>.keys     キー名の索引（値は含まない）
  run/<id>.sock            動作中のセッションのソケット（秘密はディスクに置かない）
```

リポジトリの構成：`cmd/kc-env/`（Go のソース、テスト、`locales/`）、`completions/`、`docs/`。`bin/`、鍵や保管庫のファイルは `.gitignore` で除外しています。

## 開発

```sh
go test ./...     # 単体テストと翻訳のチェック。Secure Enclave は不要（テストはソフトウェアの鍵を使う。本体はこの鍵を受け付けない）
go vet ./...
```

## サードパーティのソフトウェア

kc-env のバイナリには、次の Go モジュールが静的にリンクされています（バージョンは `go.sum` で固定）。

| モジュール | ライセンス |
|---|---|
| [filippo.io/age](https://github.com/FiloSottile/age)、filippo.io/hpke | BSD 3-Clause |
| golang.org/x/crypto、x/sys、x/term、x/text | BSD 3-Clause |
| [github.com/nicksnyder/go-i18n/v2](https://github.com/nicksnyder/go-i18n) | MIT |
| [github.com/BurntSushi/toml](https://github.com/BurntSushi/toml) | MIT |

kc-env には [age-plugin-se](https://github.com/remko/age-plugin-se)（MIT、Remko Tronçon）も必要です。[docs/build.ja.md](docs/build.ja.md) の手順で別にビルドします。このリポジトリには含まれていません。

ビルドしたバイナリを再配布する場合は、これらのライセンスを同梱してください。

## ライセンス

[0BSD](LICENSE)
