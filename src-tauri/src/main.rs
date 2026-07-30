#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    let arguments = std::env::args().skip(1).collect::<Vec<_>>();
    if arguments == ["--version"] {
        println!("NekoSSH {}", env!("CARGO_PKG_VERSION"));
        return;
    }
    if arguments == ["--self-test"] {
        match nekossh_lib::production_self_test_receipt() {
            Ok(receipt) => {
                println!("{receipt}");
                return;
            }
            Err(error) => {
                eprintln!("NekoSSH 原生运行时自检失败：{error}");
                std::process::exit(1);
            }
        }
    }
    if !arguments.is_empty() {
        eprintln!("仅支持 --version 与 --self-test。");
        std::process::exit(2);
    }
    nekossh_lib::run();
}
