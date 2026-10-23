pub const DISABLED_FEATURES: &[&str] = &[
    "shell_tool",
    "unified_exec",
    "apps",
    "plugins",
    "remote_plugin",
    "plugin_sharing",
    "skill_mcp_dependency_install",
    "tool_suggest",
    "in_app_browser",
    "browser_use",
    "browser_use_full_cdp_access",
    "browser_use_external",
    "computer_use",
    "image_generation",
    "hooks",
    "multi_agent",
    "code_mode_host",
    "shell_snapshot",
    "mentions_v2",
    "guardian_approval",
    "goals",
    "tool_call_mcp_elicitation",
    "auth_elicitation",
    "workspace_dependencies",
    "remote_compaction_v2",
    "remote_control",
    "realtime_conversation",
];

pub fn validate(mut enabled: impl FnMut(&str) -> Option<bool>) -> std::io::Result<()> {
    for key in DISABLED_FEATURES {
        if enabled(key) != Some(false) {
            return Err(std::io::Error::new(
                std::io::ErrorKind::PermissionDenied,
                "read-only bundle requires all tool and plugin features disabled; managed settings will not be overridden",
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compatible_policy_passes() {
        assert!(validate(|_| Some(false)).is_ok());
    }

    #[test]
    fn every_enabled_or_unknown_feature_is_refused() {
        for key in DISABLED_FEATURES {
            for value in [Some(true), None] {
                assert!(validate(|k| if k == *key { value } else { Some(false) }).is_err());
            }
        }
    }
}
