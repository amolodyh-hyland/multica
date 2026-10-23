use super::*;
use codex_config::test_support::CloudConfigBundleFixture;

#[tokio::test]
async fn read_only_bundle_checks_cloud_pins_on_native_config_load() {
    let home = tempfile::tempdir().unwrap();
    let config = format!(
        "[features]\n{}",
        read_only_bundle::DISABLED_FEATURES
            .iter()
            .map(|key| format!("{key} = false\n"))
            .collect::<String>()
    );
    std::fs::write(home.path().join("config.toml"), &config).unwrap();
    let load = |requirements: &str| {
        ConfigManager::new_for_tests(
            home.path().to_path_buf(),
            vec![],
            LoaderOverrides::without_managed_config_for_tests(),
            CloudConfigBundleFixture::loader_with_enterprise_requirement(requirements),
        )
        .with_read_only_bundle(true)
    };
    let compatible = load("[features]\napps = false\nplugins = false\nremote_plugin = false\n");
    assert!(compatible.load_latest_config(Some(home.path().to_path_buf())).await.is_ok());
    for key in read_only_bundle::DISABLED_FEATURES {
        let incompatible = load(&format!("[features]\n{key} = true\n"));
        let error = incompatible.load_latest_config(Some(home.path().to_path_buf())).await.unwrap_err();
        assert_eq!(error.kind(), std::io::ErrorKind::PermissionDenied, "{key}");
        assert_eq!(std::fs::read_to_string(home.path().join("config.toml")).unwrap(), config);
    }
}

#[tokio::test]
async fn read_only_bundle_checks_cloud_mcp_on_every_native_config_load() {
    let home = tempfile::tempdir().unwrap();
    let config = format!(
        "[features]\n{}",
        read_only_bundle::DISABLED_FEATURES
            .iter()
            .map(|key| format!("{key} = false\n"))
            .collect::<String>()
    );
    std::fs::write(home.path().join("config.toml"), &config).unwrap();
    for enabled in [false, true] {
        let cloud = format!("[mcp_servers.enterprise]\ncommand = '/must-not-spawn'\nenabled = {enabled}\n");
        let manager = ConfigManager::new_for_tests(
            home.path().to_path_buf(),
            vec![],
            LoaderOverrides::without_managed_config_for_tests(),
            CloudConfigBundleFixture::loader_with_enterprise_config(cloud),
        )
        .with_read_only_bundle(true);
        let error = manager.load_latest_config(Some(home.path().to_path_buf())).await.unwrap_err();
        assert_eq!(error.kind(), std::io::ErrorKind::PermissionDenied);
    }
    // A file update after attestation must be refused on the next native load.
    let manager = ConfigManager::without_managed_config_for_tests(home.path().to_path_buf())
        .with_read_only_bundle(true);
    assert!(manager.load_latest_config(Some(home.path().to_path_buf())).await.is_ok());
    std::fs::write(home.path().join("config.toml"), format!("{config}\n[mcp_servers.changed]\ncommand='/must-not-spawn'\n")).unwrap();
    let error = manager.load_latest_config(Some(home.path().to_path_buf())).await.unwrap_err();
    assert_eq!(error.kind(), std::io::ErrorKind::PermissionDenied);
}
