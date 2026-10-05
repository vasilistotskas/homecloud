fn main() {
    tauri_build::try_build(tauri_build::Attributes::new().app_manifest(
        // Only these app commands exist; capabilities grant them per window.
        tauri_build::AppManifest::new().commands(&[
            "agent_endpoint",
            "start_agent",
            "reveal_in_folder",
        ]),
    ))
    .expect("failed to run tauri-build");
}
