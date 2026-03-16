use anyhow::{bail, Context, Result};
use std::io::{BufRead, BufReader, Write};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};

use crate::types::{RpcRequest, RpcResponse};

/// Manages the codereviewserver child process and JSON-RPC communication.
pub struct RpcClient {
    child: Child,
    reader: BufReader<std::process::ChildStdout>,
    next_id: AtomicU64,
}

impl RpcClient {
    /// Spawn the codereviewserver process and set up stdio communication.
    pub fn new() -> Result<Self> {
        let server_bin = which_server()?;

        let mut child = Command::new(&server_bin)
            .arg("--server")
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::null())
            .spawn()
            .with_context(|| format!("Failed to spawn server: {server_bin}"))?;

        let stdout = child
            .stdout
            .take()
            .context("Failed to capture server stdout")?;

        Ok(Self {
            child,
            reader: BufReader::new(stdout),
            next_id: AtomicU64::new(1),
        })
    }

    /// Send a JSON-RPC request and read the response.
    pub fn call(&mut self, method: &str, params: serde_json::Value) -> Result<serde_json::Value> {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let request = RpcRequest {
            method: format!("RPCHandler.{method}"),
            params: vec![params],
            id,
        };

        let stdin = self
            .child
            .stdin
            .as_mut()
            .context("Server stdin not available")?;

        let payload = serde_json::to_string(&request)?;
        stdin.write_all(payload.as_bytes())?;
        stdin.write_all(b"\n")?;
        stdin.flush()?;

        // Read one line of JSON response
        let mut line = String::new();
        self.reader.read_line(&mut line)?;

        let resp: RpcResponse =
            serde_json::from_str(&line).context("Failed to parse JSON-RPC response")?;

        if let Some(err) = resp.error {
            bail!("RPC error: {err}");
        }

        resp.result
            .ok_or_else(|| anyhow::anyhow!("Empty result from RPC"))
    }
}

impl Drop for RpcClient {
    fn drop(&mut self) {
        // Close stdin to signal the server to exit, then wait.
        drop(self.child.stdin.take());
        let _ = self.child.wait();
    }
}

/// Find the server binary: try `crs` first, then `codereviewserver`.
fn which_server() -> Result<String> {
    for name in &["crs", "codereviewserver"] {
        if let Ok(output) = Command::new("which").arg(name).output() {
            if output.status.success() {
                let path = String::from_utf8_lossy(&output.stdout).trim().to_string();
                if !path.is_empty() {
                    return Ok(path);
                }
            }
        }
    }
    bail!(
        "Could not find 'crs' or 'codereviewserver' on PATH. \
         Please install the server binary first."
    )
}

#[cfg(test)]
mod tests {
    use crate::types::RpcRequest;

    #[test]
    fn test_rpc_request_serialization() {
        let request = RpcRequest {
            method: "RPCHandler.GetAllReviews".to_string(),
            params: vec![serde_json::json!({})],
            id: 1,
        };

        let json = serde_json::to_string(&request).expect("Failed to serialize");
        assert!(json.contains("GetAllReviews"));
        assert!(json.contains("\"id\":1"));
    }

    #[test]
    fn test_rpc_request_with_params() {
        let request = RpcRequest {
            method: "RPCHandler.GetPR".to_string(),
            params: vec![serde_json::json!({
                "Owner": "octocat",
                "Repo": "hello",
                "Number": 42
            })],
            id: 2,
        };

        let json = serde_json::to_string(&request).expect("Failed to serialize");
        assert!(json.contains("octocat"));
        assert!(json.contains("hello"));
        assert!(json.contains("42"));
    }
}
