//! se-env-ui: the dialogs of se-env on Windows (macOS uses osascript).
//!
//!   se-env-ui secret  <title> <label>             hidden input; the value goes to stdout
//!   se-env-ui confirm <title> <message>           exit 0 for yes
//!   se-env-ui alert   <lockfile> <title> <message>  one alert per lock file, kept while shown

#[cfg(windows)]
fn main() {
    use std::io::Write;
    // A secret passes through this process: keep the same user out of it.
    // The dialogs that carry no secret still work if that fails.
    let hardened = sewin::process::harden_self();
    sewin::ui::init_dpi();
    let args: Vec<String> = std::env::args().skip(1).collect();
    let code = match args
        .iter()
        .map(String::as_str)
        .collect::<Vec<_>>()
        .as_slice()
    {
        ["secret", ..] if hardened.is_err() => {
            eprintln!(
                "se-env-ui: this process could not be protected from other programs; not asking for a secret"
            );
            1
        }
        ["secret", title, label] => match sewin::ui::ask_secret(title, label) {
            Some(v) if !v.is_empty() => {
                let mut out = std::io::stdout().lock();
                let ok = out
                    .write_all(v.as_bytes())
                    .and_then(|_| out.flush())
                    .is_ok();
                drop(v);
                if ok { 0 } else { 1 }
            }
            _ => 1,
        },
        ["confirm", title, message] => {
            if sewin::ui::confirm(title, message) {
                0
            } else {
                1
            }
        }
        ["alert", lock, title, message] => {
            use std::os::windows::io::AsRawHandle;
            let Ok(f) = std::fs::OpenOptions::new()
                .read(true)
                .write(true)
                .create(true)
                .truncate(false)
                .open(lock)
            else {
                std::process::exit(1)
            };
            // SAFETY: f is open until the end of this block.
            if unsafe { sewin::lock::lock(f.as_raw_handle(), true, false) }.is_err() {
                std::process::exit(0) // this alert is already on screen
            }
            sewin::ui::alert(title, message);
            drop(f);
            0
        }
        _ => {
            eprintln!(
                "usage: se-env-ui secret <title> <label> | confirm <title> <message> | alert <lockfile> <title> <message>"
            );
            2
        }
    };
    std::process::exit(code)
}

#[cfg(not(windows))]
fn main() {
    eprintln!("se-env-ui runs on Windows only");
    std::process::exit(1)
}
