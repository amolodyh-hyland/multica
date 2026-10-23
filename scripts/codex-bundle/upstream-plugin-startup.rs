    pub fn maybe_start_plugin_startup_tasks_for_config(
        self: &Arc<Self>,
        config: &PluginsConfigInput,
        auth_manager: Arc<AuthManager>,
        on_effective_plugins_changed: Option<Arc<dyn Fn() + Send + Sync + 'static>>,
    ) {
        if config.plugins_enabled {
            let use_remote_global_catalog =
                config.remote_plugin_enabled && auth_manager.current_auth_uses_codex_backend();
            if !use_remote_global_catalog {
                self.start_curated_repo_sync();
            }
            let should_spawn_marketplace_auto_upgrade = {
                let mut state = match self.configured_marketplace_upgrade_state.write() {
                    Ok(state) => state,
                    Err(err) => err.into_inner(),
                };
                if state.in_flight {
                    false
                } else {
                    state.in_flight = true;
                    true
                }
            };
            if should_spawn_marketplace_auto_upgrade {
                let manager = Arc::clone(self);
                let config = config.clone();
                if let Err(err) = std::thread::Builder::new()
                    .name("plugins-marketplace-auto-upgrade".to_string())
                    .spawn(move || {
                        let outcome = manager.upgrade_configured_marketplaces_for_config(
                            &config, /*marketplace_name*/ None,
                        );
                        match outcome {
                            Ok(outcome) => {
                                for error in outcome.errors {
                                    warn!(
                                        marketplace = error.marketplace_name,
                                        error = %error.message,
                                        "failed to auto-upgrade configured marketplace"
                                    );
                                }
                            }
                            Err(err) => {
                                warn!("failed to auto-upgrade configured marketplaces: {err}");
                            }
                        }

                        let mut state = match manager.configured_marketplace_upgrade_state.write() {
                            Ok(state) => state,
                            Err(err) => err.into_inner(),
                        };
                        state.in_flight = false;
                    })
                {
                    let mut state = match self.configured_marketplace_upgrade_state.write() {
                        Ok(state) => state,
                        Err(err) => err.into_inner(),
                    };
                    state.in_flight = false;
                    warn!("failed to start configured marketplace auto-upgrade task: {err}");
                }
            }
            let config_for_remote_sync = config.clone();
            let manager = Arc::clone(self);
            let auth_manager_for_remote_sync = auth_manager.clone();
            let on_effective_plugins_changed = on_effective_plugins_changed.clone();
            tokio::spawn(async move {
                let auth = auth_manager_for_remote_sync.auth().await;
                manager.maybe_start_remote_plugin_caches_refresh(
                    &config_for_remote_sync,
                    auth.clone(),
                    on_effective_plugins_changed.clone(),
                );
                manager.maybe_start_remote_installed_plugin_bundle_sync(
                    &config_for_remote_sync,
                    auth.clone(),
                    on_effective_plugins_changed,
                );
                if config_for_remote_sync.remote_plugin_enabled {
                    match crate::remote::fetch_and_cache_global_remote_plugin_catalog(
                        manager.codex_home.as_path(),
                        &remote_plugin_service_config(&config_for_remote_sync),
                        auth.as_ref(),
                    )
                    .await
                    {
                        Ok(()) => {}
                        Err(
                            RemotePluginCatalogError::AuthRequired
                            | RemotePluginCatalogError::UnsupportedAuthMode,
                        ) => {}
                        Err(err) => {
                            warn!(
                                error = %err,
                                "failed to warm remote plugin catalog cache"
                            );
                        }
                    }
                }
            });

            let config_for_featured_plugins = config.clone();
            let manager = Arc::clone(self);
            tokio::spawn(async move {
                let auth = auth_manager.auth().await;
                if let Err(err) = manager
                    .featured_plugin_ids_for_config(&config_for_featured_plugins, auth.as_ref())
                    .await
                {
                    warn!(
                        error = %err,
                        "failed to warm featured plugin ids cache"
                    );
                }
            });
        }
    }

