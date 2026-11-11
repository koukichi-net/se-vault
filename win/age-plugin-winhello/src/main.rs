//! age-plugin-winhello: an age plugin whose key is behind Windows Hello.
//!
//!   age-plugin-winhello keygen [-o FILE]   create a Hello key and print its identity
//!   age-plugin-winhello recipients         identity lines on stdin -> recipients
//!   age-plugin-winhello delete             identity lines on stdin -> delete their Hello keys
//!   age-plugin-winhello --age-plugin=…     the age plugin protocol (run by age / se-env)
//!
//! The key derivation and formats are in winhello-core; the design is in
//! docs/design-windows-hello.ja.md.

#[cfg(windows)]
mod plugin;

#[cfg(windows)]
fn main() {
    std::process::exit(plugin::main())
}

#[cfg(not(windows))]
fn main() {
    eprintln!("age-plugin-winhello runs on Windows only");
    std::process::exit(1)
}
