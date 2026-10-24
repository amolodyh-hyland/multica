package libraryimport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

func Upload(ctx context.Context, client *http.Client, headers http.Header, input Request) (Receipt, error) {
	var receipt Receipt
	data, err := json.Marshal(input)
	if err != nil {
		return receipt, err
	}
	input, err = DecodeRequest(data)
	if err != nil {
		return receipt, err
	}
	if err := input.VerifyConsent(time.Now()); err != nil {
		return receipt, err
	}
	if err := input.Verify(); err != nil {
		return receipt, err
	}
	if client == nil {
		return receipt, errors.New("missing_transport")
	}
	scopedClient := *client
	scopedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	expires, _ := time.Parse(timestampLayout, input.Consent.ExpiresAt)
	ctx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	origin := input.Preview.Destination.Origin
	send := func(method, path string, body []byte) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("X-Workspace-ID", input.Preview.Destination.WorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		return scopedClient.Do(req)
	}
	resp, err := send(http.MethodGet, "/api/private-library/capabilities", nil)
	if err != nil {
		return receipt, err
	}
	var capability Capability
	err = json.NewDecoder(io.LimitReader(resp.Body, 16*1024)).Decode(&capability)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || !capability.Supported {
		return receipt, errors.New("private_import_unsupported")
	}
	if capability.Destination != input.Preview.Destination {
		return receipt, errors.New("destination_changed")
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if err := input.VerifyConsent(time.Now()); err != nil {
		return receipt, err
	}
	resp, err = send(http.MethodPost, "/api/private-library/imports", data)
	if err != nil {
		return receipt, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return receipt, fmt.Errorf("private_import_failed: %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16*1024)).Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	if receipt.AuthorityID != input.Preview.Destination.ServerID || receipt.PreviewDigest != input.Consent.PreviewDigest || receipt.ContentHash != input.Preview.Entry.ContentHash || !ValidHash(receipt.TransportHash) || receipt.ObjectID == "" || receipt.ApprovalRef == "" || receipt.Status != "stored" || receipt.ReasonCode != "unsupported_provider" {
		return Receipt{}, errors.New("invalid_receipt")
	}
	return receipt, nil
}
