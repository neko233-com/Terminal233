#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    let arguments = std::env::args().skip(1).collect::<Vec<_>>();
    if arguments == ["--version"] {
        println!("Terminal233 {}", env!("CARGO_PKG_VERSION"));
        return;
    }
    if arguments == ["--self-test"] {
        match terminal233_lib::production_self_test_receipt() {
            Ok(receipt) => {
                println!("{receipt}");
                return;
            }
            Err(error) => {
                eprintln!("Terminal233 原生运行时自检失败：{error}");
                std::process::exit(1);
            }
        }
    }
    if !arguments.is_empty() {
        eprintln!("仅支持 --version 与 --self-test。");
        std::process::exit(2);
    }
    if let Err(error) = terminal233_lib::run_rdesktop() {
        eprintln!("Terminal233 启动失败：{error}");
        std::process::exit(1);
    }
}
