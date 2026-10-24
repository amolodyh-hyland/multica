package libraryimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

// RemovalPreview is displayed separately from import consent. It identifies
// the destination object and includes the original destination and file list.
type RemovalPreview struct {
	Operation string  `json:"operation"`
	ObjectID  string  `json:"objectId"`
	Import    Preview `json:"import"`
}

type RemovalRequest struct {
	Preview RemovalPreview `json:"preview"`
	Consent Consent        `json:"consent"`
}

func (p RemovalPreview) Digest() (string, error) {
	return RecordDigest("private-library-removal-v1", p)
}

func (p RemovalPreview) Validate() error {
	if p.Operation != "private_import_remove" || !knowledge.ValidID(p.ObjectID) {
		return errors.New("invalid_removal_preview")
	}
	return p.Import.Validate()
}

func ConfirmRemoval(p RemovalPreview, displayedDigest, actorID string, now time.Time) (Consent, error) {
	if err := p.Validate(); err != nil {
		return Consent{}, err
	}
	digest, err := p.Digest()
	if err != nil {
		return Consent{}, err
	}
	if digest != displayedDigest || actorID != p.Import.Destination.OwnerID {
		return Consent{}, errors.New("removal_consent_mismatch")
	}
	now = now.UTC().Truncate(time.Millisecond)
	return Consent{ActorID: actorID, PreviewDigest: digest, ApprovedAt: now.Format(timestampLayout), ExpiresAt: now.Add(ConsentLifetime).Format(timestampLayout)}, nil
}

func (r RemovalRequest) VerifyConsent(now time.Time) error {
	if err := r.Preview.Validate(); err != nil {
		return err
	}
	digest, err := r.Preview.Digest()
	if err != nil {
		return err
	}
	c := r.Consent
	approved, e1 := time.Parse(timestampLayout, c.ApprovedAt)
	expires, e2 := time.Parse(timestampLayout, c.ExpiresAt)
	if e1 != nil || e2 != nil || approved.Format(timestampLayout) != c.ApprovedAt || expires.Format(timestampLayout) != c.ExpiresAt || approved.After(now) || !now.Before(expires) || expires.Sub(approved) != ConsentLifetime || c.ActorID != r.Preview.Import.Destination.OwnerID || c.PreviewDigest != digest {
		return errors.New("removal_consent_mismatch_or_expired")
	}
	return nil
}

func DecodeRemovalRequest(raw []byte) (RemovalRequest, error) {
	var r RemovalRequest
	if len(raw) > MaxRequestBytes {
		return r, errors.New("payload_limit")
	}
	if _, err := CanonicalJSON(raw); err != nil {
		return r, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return r, errors.New("trailing_json")
	}
	return r, nil
}
