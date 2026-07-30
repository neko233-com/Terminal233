use std::collections::BTreeSet;
use std::env;
use std::fs::{self, File};
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Component, Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

const VERSION: &str = env!("CARGO_PKG_VERSION");
const PAYLOAD_MANIFEST: &str = "PAYLOAD_SHA256SUMS.txt";
const EXPECTED_PAYLOAD_MANIFEST_SHA256: &str = match option_env!("NEKOSSH_WEB_PAYLOAD_SHA256") {
    Some(value) => value,
    None => "",
};
const PORT_START: u16 = 7331;
const PORT_END: u16 = 7350;

fn main() {
    if let Err(error) = run() {
        eprintln!("NekoSSH Web 启动失败：{error}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let arguments = env::args().skip(1).collect::<Vec<_>>();
    if arguments.iter().any(|argument| argument == "--version") {
        println!("NekoSSH Web {VERSION}");
        return Ok(());
    }
    let self_test = arguments.iter().any(|argument| argument == "--self-test");
    let no_browser = self_test || arguments.iter().any(|argument| argument == "--no-browser");
    if arguments
        .iter()
        .any(|argument| !matches!(argument.as_str(), "--self-test" | "--no-browser"))
    {
        return Err("仅支持 --version、--no-browser 与 --self-test。".into());
    }

    let executable = env::current_exe()
        .and_then(fs::canonicalize)
        .map_err(|error| format!("无法定位启动器：{error}"))?;
    let root = executable
        .parent()
        .ok_or("启动器没有父目录。")?
        .to_path_buf();
    let payload = verify_payload(&root)?;
    let node = canonical_payload_file(&root, Path::new("runtime/node.exe"))?;
    let app = canonical_payload_directory(&root, Path::new("app"))?;
    let server = canonical_payload_file(&root, Path::new("app/server/ssh2js-server.mjs"))?;
    let web_root = canonical_payload_directory(&root, Path::new("app/dist"))?;
    let external_app = external_process_path(&app);
    let external_server = external_process_path(&server);
    let external_web_root = external_process_path(&web_root);
    let port = available_port()?;
    let url = format!("http://127.0.0.1:{port}/");

    let mut command = Command::new(node);
    harden_node_environment(&mut command);
    command
        .arg("--permission")
        .arg(format!(
            "--allow-fs-read={}",
            external_app.to_string_lossy().replace('\\', "/")
        ))
        .arg("--no-addons")
        .arg("--no-global-search-paths")
        .arg("--disable-proto=throw")
        .arg(external_server)
        .current_dir(&external_app)
        .env("NEKOSSH_BACKEND_PORT", port.to_string())
        .env("NEKOSSH_FRONTEND_PORT", port.to_string())
        .env("NEKOSSH_WEB_ROOT", &external_web_root)
        .stdin(Stdio::inherit())
        .stdout(Stdio::inherit())
        .stderr(Stdio::inherit());
    let child = command
        .spawn()
        .map_err(|error| format!("无法启动内置 Node ssh2js 后端：{error}"))?;
    let mut child = ChildGuard::new(child);
    wait_until_ready(port, child.child_mut())?;

    if self_test {
        run_self_test(port, &payload)?;
        child.terminate()?;
        wait_until_stopped(port)?;
        println!(
            "{{\"ok\":true,\"version\":\"{VERSION}\",\"runtime\":\"node-ssh2js\",\"persistence\":\"memory_only\"}}"
        );
        return Ok(());
    }

    println!("NekoSSH Web: {url}");
    println!("NekoSSH MCP: http://127.0.0.1:{port}/mcp");
    println!("保持此窗口打开；关闭窗口或按 Ctrl+C 会清空全部临时连接与凭据。");
    if !no_browser {
        open_default_browser(&url)?;
    }
    let status = child.wait()?;
    if status.success() {
        Ok(())
    } else {
        Err(format!("本地 Node ssh2js 后端异常退出：{status}"))
    }
}

fn harden_node_environment(command: &mut Command) {
    for variable in [
        "NODE_OPTIONS",
        "NODE_PATH",
        "NODE_EXTRA_CA_CERTS",
        "NODE_COMPILE_CACHE",
        "NODE_COMPILE_CACHE_PORTABLE",
        "NODE_REDIRECT_WARNINGS",
        "NODE_REPL_EXTERNAL_MODULE",
        "NODE_V8_COVERAGE",
        "NEKOSSH_MCP_TOKEN",
        "NEKOSSH_DEBUG",
    ] {
        command.env_remove(variable);
    }
    command.env("NODE_ENV", "production");
}

fn verify_payload(root: &Path) -> Result<Vec<String>, String> {
    let expected = EXPECTED_PAYLOAD_MANIFEST_SHA256.to_ascii_lowercase();
    if expected.len() != 64
        || expected.bytes().any(|byte| !byte.is_ascii_hexdigit())
        || expected.bytes().all(|byte| byte == b'0')
    {
        return Err("启动器没有嵌入正式 payload manifest 哈希。".into());
    }
    let manifest_path = root.join(PAYLOAD_MANIFEST);
    let manifest_bytes =
        fs::read(&manifest_path).map_err(|error| format!("无法读取 payload manifest：{error}"))?;
    if hex_sha256(&manifest_bytes) != expected {
        return Err("payload manifest 与签名启动器不匹配。".into());
    }
    let manifest = std::str::from_utf8(&manifest_bytes)
        .map_err(|_| "payload manifest 不是 UTF-8。".to_string())?;
    let mut declared = BTreeSet::new();
    let mut web_assets = Vec::new();
    for line in manifest.lines().filter(|line| !line.is_empty()) {
        let (expected_hash, relative_path) = line
            .split_once("  *")
            .ok_or("payload manifest 行格式无效。")?;
        if expected_hash.len() != 64 || expected_hash.bytes().any(|byte| !byte.is_ascii_hexdigit())
        {
            return Err("payload manifest 包含无效 SHA-256。".into());
        }
        let relative_path = validate_relative_payload_path(relative_path)?;
        if !declared.insert(relative_path.clone()) {
            return Err(format!("payload manifest 包含重复文件：{relative_path}"));
        }
        let file = canonical_payload_file(root, Path::new(&relative_path))?;
        let actual_hash = hash_file(&file)?;
        if actual_hash != expected_hash.to_ascii_lowercase() {
            return Err(format!("payload 文件哈希不匹配：{relative_path}"));
        }
        if relative_path.starts_with("app/dist/") {
            web_assets.push(relative_path);
        }
    }

    for required in [
        "LICENSE",
        "THIRD_PARTY_NOTICES.txt",
        "VERSION.json",
        "runtime/LICENSE",
        "runtime/node.exe",
        "app/dist/index.html",
        "app/server/ssh2js-server.mjs",
        "app/server/static-web.mjs",
        "app/node_modules/ssh2/package.json",
    ] {
        if !declared.contains(required) {
            return Err(format!("payload manifest 缺少必需文件：{required}"));
        }
    }
    let actual = collect_payload_files(root)?;
    if actual != declared {
        return Err("payload 文件集合与 manifest 不完全一致。".into());
    }
    Ok(web_assets)
}

fn collect_payload_files(root: &Path) -> Result<BTreeSet<String>, String> {
    let mut files = BTreeSet::new();
    let mut directories = vec![root.to_path_buf()];
    while let Some(directory) = directories.pop() {
        for entry in
            fs::read_dir(&directory).map_err(|error| format!("无法枚举 payload：{error}"))?
        {
            let entry = entry.map_err(|error| format!("无法读取 payload 条目：{error}"))?;
            let file_type = entry
                .file_type()
                .map_err(|error| format!("无法读取 payload 类型：{error}"))?;
            if file_type.is_symlink() {
                return Err(format!("payload 禁止符号链接：{}", entry.path().display()));
            }
            if file_type.is_dir() {
                directories.push(entry.path());
                continue;
            }
            if !file_type.is_file() {
                return Err(format!(
                    "payload 含有非普通文件：{}",
                    entry.path().display()
                ));
            }
            let relative = entry
                .path()
                .strip_prefix(root)
                .map_err(|_| "payload 条目逃逸根目录。")?
                .to_string_lossy()
                .replace('\\', "/");
            if relative != "NekoSSH-Web.exe" && relative != PAYLOAD_MANIFEST {
                files.insert(relative);
            }
        }
    }
    Ok(files)
}

fn validate_relative_payload_path(value: &str) -> Result<String, String> {
    if value.is_empty() || value.contains('\\') || value.contains('\0') || value.contains(':') {
        return Err("payload manifest 包含不安全路径。".into());
    }
    let path = Path::new(value);
    if path.is_absolute()
        || path
            .components()
            .any(|component| !matches!(component, Component::Normal(_)))
    {
        return Err("payload manifest 包含不安全路径。".into());
    }
    Ok(value.to_string())
}

fn canonical_payload_file(root: &Path, relative: &Path) -> Result<PathBuf, String> {
    let path = fs::canonicalize(root.join(relative))
        .map_err(|error| format!("缺少 payload 文件 {}：{error}", relative.display()))?;
    if !path.starts_with(root) || !path.is_file() {
        return Err(format!(
            "payload 文件逃逸或类型无效：{}",
            relative.display()
        ));
    }
    Ok(path)
}

fn canonical_payload_directory(root: &Path, relative: &Path) -> Result<PathBuf, String> {
    let path = fs::canonicalize(root.join(relative))
        .map_err(|error| format!("缺少 payload 目录 {}：{error}", relative.display()))?;
    if !path.starts_with(root) || !path.is_dir() {
        return Err(format!(
            "payload 目录逃逸或类型无效：{}",
            relative.display()
        ));
    }
    Ok(path)
}

#[cfg(windows)]
fn external_process_path(path: &Path) -> PathBuf {
    let value = path.to_string_lossy();
    value
        .strip_prefix(r"\\?\")
        .map(PathBuf::from)
        .unwrap_or_else(|| path.to_path_buf())
}

#[cfg(not(windows))]
fn external_process_path(path: &Path) -> PathBuf {
    path.to_path_buf()
}

fn available_port() -> Result<u16, String> {
    for port in PORT_START..=PORT_END {
        if TcpListener::bind(("127.0.0.1", port)).is_ok() {
            return Ok(port);
        }
    }
    Err(format!("{PORT_START}-{PORT_END} 端口均被占用。"))
}

fn wait_until_ready(port: u16, child: &mut Child) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(15);
    while Instant::now() < deadline {
        if let Some(status) = child
            .try_wait()
            .map_err(|error| format!("无法读取后端状态：{error}"))?
        {
            return Err(format!("本地 Node ssh2js 后端提前退出：{status}"));
        }
        if http_request(port, "GET", "/healthz", None, "")
            .is_ok_and(|response| response.status == 200)
        {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(100));
    }
    Err("本地 Node ssh2js 后端启动超时。".into())
}

fn run_self_test(port: u16, web_assets: &[String]) -> Result<(), String> {
    for asset in web_assets {
        let relative = asset
            .strip_prefix("app/dist/")
            .ok_or("Web 资产路径无效。")?;
        let request_path = if relative == "index.html" {
            "/".to_string()
        } else {
            format!("/{relative}")
        };
        let response = http_request(port, "GET", &request_path, None, "")?;
        if response.status != 200 || response.header("x-content-type-options") != Some("nosniff") {
            return Err(format!("Web 静态资产自检失败：{request_path}"));
        }
        if relative == "index.html"
            && !response
                .header("content-security-policy")
                .is_some_and(|value| value.contains("object-src 'none'"))
        {
            return Err("Web 入口缺少生产 CSP。".into());
        }
    }
    let origin = format!("http://127.0.0.1:{port}");
    let health = http_request(port, "GET", "/api/health", Some(&origin), "")?;
    if health.status != 200
        || !String::from_utf8_lossy(&health.body).contains("\"sshEngine\":\"ssh2js\"")
    {
        return Err("ssh2js API 健康检查失败。".into());
    }
    let session = http_request(port, "POST", "/api/browser-session", Some(&origin), "{}")?;
    if session.status != 200
        || !session
            .header("set-cookie")
            .is_some_and(|value| value.contains("HttpOnly") && value.contains("SameSite=Strict"))
    {
        return Err("同源临时浏览器会话自检失败。".into());
    }
    let rejected = http_request(
        port,
        "POST",
        "/api/browser-session",
        Some("https://attacker.invalid"),
        "{}",
    )?;
    if rejected.status != 403 {
        return Err("恶意 Origin 未被拒绝。".into());
    }
    Ok(())
}

struct HttpResponse {
    status: u16,
    headers: Vec<(String, String)>,
    body: Vec<u8>,
}

impl HttpResponse {
    fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

fn http_request(
    port: u16,
    method: &str,
    path: &str,
    origin: Option<&str>,
    body: &str,
) -> Result<HttpResponse, String> {
    let mut stream = TcpStream::connect_timeout(
        &format!("127.0.0.1:{port}")
            .parse()
            .map_err(|_| "自检地址无效。")?,
        Duration::from_secs(1),
    )
    .map_err(|error| format!("无法连接本地自检端点：{error}"))?;
    stream
        .set_read_timeout(Some(Duration::from_secs(3)))
        .map_err(|error| format!("无法设置自检超时：{error}"))?;
    let origin_header = origin
        .map(|value| format!("Origin: {value}\r\n"))
        .unwrap_or_default();
    let request = format!(
        "{method} {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n{origin_header}Content-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    );
    stream
        .write_all(request.as_bytes())
        .map_err(|error| format!("无法发送自检请求：{error}"))?;
    let mut bytes = Vec::new();
    stream
        .take(20 * 1024 * 1024)
        .read_to_end(&mut bytes)
        .map_err(|error| format!("无法读取自检响应：{error}"))?;
    let header_end = bytes
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .ok_or("自检响应缺少 HTTP 头。")?;
    let header_text =
        std::str::from_utf8(&bytes[..header_end]).map_err(|_| "自检响应头不是 UTF-8。")?;
    let mut lines = header_text.split("\r\n");
    let status = lines
        .next()
        .and_then(|line| line.split_ascii_whitespace().nth(1))
        .and_then(|value| value.parse::<u16>().ok())
        .ok_or("自检响应状态无效。")?;
    let headers = lines
        .filter_map(|line| line.split_once(':'))
        .map(|(name, value)| (name.trim().to_string(), value.trim().to_string()))
        .collect();
    Ok(HttpResponse {
        status,
        headers,
        body: bytes[header_end + 4..].to_vec(),
    })
}

fn wait_until_stopped(port: u16) -> Result<(), String> {
    let deadline = Instant::now() + Duration::from_secs(5);
    while Instant::now() < deadline {
        if TcpStream::connect(("127.0.0.1", port)).is_err() {
            return Ok(());
        }
        thread::sleep(Duration::from_millis(50));
    }
    Err("自检结束后本地端口仍在监听。".into())
}

struct ChildGuard(Option<Child>);

impl ChildGuard {
    fn new(child: Child) -> Self {
        Self(Some(child))
    }

    fn child_mut(&mut self) -> &mut Child {
        self.0.as_mut().expect("child is present")
    }

    fn terminate(&mut self) -> Result<(), String> {
        if let Some(mut child) = self.0.take() {
            child
                .kill()
                .map_err(|error| format!("无法终止自检后端：{error}"))?;
            child
                .wait()
                .map_err(|error| format!("无法等待自检后端退出：{error}"))?;
        }
        Ok(())
    }

    fn wait(&mut self) -> Result<std::process::ExitStatus, String> {
        let mut child = self.0.take().expect("child is present");
        child
            .wait()
            .map_err(|error| format!("无法等待后端退出：{error}"))
    }
}

impl Drop for ChildGuard {
    fn drop(&mut self) {
        if let Some(child) = self.0.as_mut() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

fn hash_file(path: &Path) -> Result<String, String> {
    let mut file =
        File::open(path).map_err(|error| format!("无法读取 {}：{error}", path.display()))?;
    let mut hasher = Sha256::new();
    let mut buffer = [0_u8; 64 * 1024];
    loop {
        let read = file
            .read(&mut buffer)
            .map_err(|error| format!("无法读取 {}：{error}", path.display()))?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
    }
    Ok(hex(&hasher.finalize()))
}

fn hex_sha256(bytes: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(bytes);
    hex(&hasher.finalize())
}

fn hex(bytes: &[u8]) -> String {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        output.push(DIGITS[(byte >> 4) as usize] as char);
        output.push(DIGITS[(byte & 0x0f) as usize] as char);
    }
    output
}

struct Sha256 {
    state: [u32; 8],
    buffer: [u8; 64],
    buffer_len: usize,
    bit_len: u64,
}

impl Sha256 {
    fn new() -> Self {
        Self {
            state: [
                0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab,
                0x5be0cd19,
            ],
            buffer: [0; 64],
            buffer_len: 0,
            bit_len: 0,
        }
    }

    fn update(&mut self, mut input: &[u8]) {
        self.bit_len = self
            .bit_len
            .wrapping_add((input.len() as u64).wrapping_mul(8));
        if self.buffer_len != 0 {
            let copied = (64 - self.buffer_len).min(input.len());
            self.buffer[self.buffer_len..self.buffer_len + copied]
                .copy_from_slice(&input[..copied]);
            self.buffer_len += copied;
            input = &input[copied..];
            if self.buffer_len == 64 {
                let block = self.buffer;
                self.transform(&block);
                self.buffer_len = 0;
            } else {
                return;
            }
        }
        while input.len() >= 64 {
            let block: &[u8; 64] = input[..64].try_into().expect("64-byte block");
            self.transform(block);
            input = &input[64..];
        }
        self.buffer[..input.len()].copy_from_slice(input);
        self.buffer_len = input.len();
    }

    fn finalize(mut self) -> [u8; 32] {
        self.buffer[self.buffer_len] = 0x80;
        if self.buffer_len >= 56 {
            self.buffer[self.buffer_len + 1..].fill(0);
            let block = self.buffer;
            self.transform(&block);
            self.buffer = [0; 64];
        } else {
            self.buffer[self.buffer_len + 1..56].fill(0);
        }
        self.buffer[56..].copy_from_slice(&self.bit_len.to_be_bytes());
        let block = self.buffer;
        self.transform(&block);
        let mut output = [0_u8; 32];
        for (chunk, word) in output.chunks_exact_mut(4).zip(self.state) {
            chunk.copy_from_slice(&word.to_be_bytes());
        }
        output
    }

    fn transform(&mut self, block: &[u8; 64]) {
        const K: [u32; 64] = [
            0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4,
            0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe,
            0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f,
            0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
            0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc,
            0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
            0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116,
            0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
            0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7,
            0xc67178f2,
        ];
        let mut words = [0_u32; 64];
        for (index, chunk) in block.chunks_exact(4).enumerate() {
            words[index] = u32::from_be_bytes(chunk.try_into().expect("four bytes"));
        }
        for index in 16..64 {
            let s0 = words[index - 15].rotate_right(7)
                ^ words[index - 15].rotate_right(18)
                ^ (words[index - 15] >> 3);
            let s1 = words[index - 2].rotate_right(17)
                ^ words[index - 2].rotate_right(19)
                ^ (words[index - 2] >> 10);
            words[index] = words[index - 16]
                .wrapping_add(s0)
                .wrapping_add(words[index - 7])
                .wrapping_add(s1);
        }
        let [mut a, mut b, mut c, mut d, mut e, mut f, mut g, mut h] = self.state;
        for index in 0..64 {
            let sigma1 = e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25);
            let choice = (e & f) ^ ((!e) & g);
            let first = h
                .wrapping_add(sigma1)
                .wrapping_add(choice)
                .wrapping_add(K[index])
                .wrapping_add(words[index]);
            let sigma0 = a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22);
            let majority = (a & b) ^ (a & c) ^ (b & c);
            let second = sigma0.wrapping_add(majority);
            h = g;
            g = f;
            f = e;
            e = d.wrapping_add(first);
            d = c;
            c = b;
            b = a;
            a = first.wrapping_add(second);
        }
        for (state, value) in self.state.iter_mut().zip([a, b, c, d, e, f, g, h]) {
            *state = state.wrapping_add(value);
        }
    }
}

#[cfg(windows)]
fn open_default_browser(url: &str) -> Result<(), String> {
    use std::os::windows::ffi::OsStrExt;
    use std::ptr;

    #[link(name = "shell32")]
    unsafe extern "system" {
        fn ShellExecuteW(
            window: *mut core::ffi::c_void,
            operation: *const u16,
            file: *const u16,
            parameters: *const u16,
            directory: *const u16,
            show_command: i32,
        ) -> isize;
    }

    let operation = std::ffi::OsStr::new("open")
        .encode_wide()
        .chain(Some(0))
        .collect::<Vec<_>>();
    let target = std::ffi::OsStr::new(url)
        .encode_wide()
        .chain(Some(0))
        .collect::<Vec<_>>();
    // SAFETY: Both strings are NUL-terminated and remain alive for the duration of ShellExecuteW.
    let result = unsafe {
        ShellExecuteW(
            ptr::null_mut(),
            operation.as_ptr(),
            target.as_ptr(),
            ptr::null(),
            ptr::null(),
            1,
        )
    };
    if result <= 32 {
        Err(format!(
            "无法打开默认浏览器（ShellExecuteW={result}），请手动复制上方地址。"
        ))
    } else {
        Ok(())
    }
}

#[cfg(not(windows))]
fn open_default_browser(_url: &str) -> Result<(), String> {
    Err("NekoSSH Web Windows 启动器只能在 Windows 上打开浏览器。".into())
}

#[cfg(test)]
mod tests {
    use super::{Sha256, harden_node_environment, hex, hex_sha256, validate_relative_payload_path};
    use std::ffi::OsStr;
    use std::process::Command;

    #[test]
    fn sha256_matches_standard_vectors_and_streaming() {
        assert_eq!(
            hex_sha256(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        let mut hasher = Sha256::new();
        hasher.update(b"a");
        hasher.update(b"bc");
        assert_eq!(
            hex(&hasher.finalize()),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }

    #[test]
    fn payload_paths_are_strictly_relative() {
        assert_eq!(
            validate_relative_payload_path("app/dist/index.html").unwrap(),
            "app/dist/index.html"
        );
        for invalid in [
            "",
            "../secret",
            "app/../secret",
            "C:/secret",
            "/root",
            "app\\file",
        ] {
            assert!(
                validate_relative_payload_path(invalid).is_err(),
                "{invalid}"
            );
        }
    }

    #[test]
    fn node_environment_rejects_inherited_injection_controls() {
        let mut command = Command::new("node");
        harden_node_environment(&mut command);
        let environment = command.get_envs().collect::<Vec<_>>();
        for variable in [
            "NODE_OPTIONS",
            "NODE_PATH",
            "NODE_EXTRA_CA_CERTS",
            "NODE_COMPILE_CACHE",
            "NODE_COMPILE_CACHE_PORTABLE",
            "NODE_REDIRECT_WARNINGS",
            "NODE_REPL_EXTERNAL_MODULE",
            "NODE_V8_COVERAGE",
            "NEKOSSH_MCP_TOKEN",
            "NEKOSSH_DEBUG",
        ] {
            assert!(
                environment
                    .iter()
                    .any(|(name, value)| { *name == OsStr::new(variable) && value.is_none() })
            );
        }
        assert!(environment.iter().any(|(name, value)| {
            *name == OsStr::new("NODE_ENV") && *value == Some(OsStr::new("production"))
        }));
    }
}
