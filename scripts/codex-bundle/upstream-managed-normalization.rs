fn normalize_candidate(
    mut candidate: Features,
    pinned_features: &BTreeMap<Feature, bool>,
) -> Features {
    for (feature, enabled) in pinned_features {
        candidate.set_enabled(*feature, *enabled);
    }
    candidate.normalize_dependencies();
    candidate
}

