// Copyright © 2020 - 2024 Attestant Limited.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"

	"github.com/attestantio/go-eth2-client/api"
	"github.com/attestantio/go-eth2-client/spec"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// post sends an HTTP post request and returns the body.
func (s *Service) post(ctx context.Context, endpoint string, body io.Reader) (io.Reader, error) {
	// #nosec G404
	log := s.log.With().Str("id", fmt.Sprintf("%02x", rand.Int31())).Str("address", s.address).Str("endpoint", endpoint).Logger()
	if e := log.Trace(); e.Enabled() {
		bodyBytes, err := io.ReadAll(body)
		if err != nil {
			return nil, errors.New("failed to read request body")
		}
		body = bytes.NewReader(bodyBytes)

		e.Str("body", string(bodyBytes)).Msg("POST request")
	}

	url := urlForCall(s.base, endpoint, "")

	opCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, url.String(), body)
	if err != nil {
		return nil, errors.Join(errors.New("failed to create POST request"), err)
	}
	s.addExtraHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "go-eth2-client/0.19.10")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		go s.CheckConnectionState(ctx)
		s.monitorPostComplete(ctx, url.Path, "failed")

		return nil, errors.Join(errors.New("failed to call POST endpoint"), err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Join(errors.New("failed to read POST response"), err)
	}

	statusFamily := resp.StatusCode / 100
	if statusFamily != 2 {
		log.Trace().Int("status_code", resp.StatusCode).Str("data", string(data)).Msg("POST failed")
		s.monitorPostComplete(ctx, url.Path, "failed")

		return nil, &api.Error{
			Method:     http.MethodPost,
			StatusCode: resp.StatusCode,
			Endpoint:   endpoint,
			Data:       data,
		}
	}

	log.Trace().Str("response", string(data)).Msg("POST response")
	s.monitorPostComplete(ctx, url.Path, "succeeded")

	return bytes.NewReader(data), nil
}

// post2 sends an HTTP post request and returns the body.
//
//nolint:unparam
func (s *Service) post2(ctx context.Context,
	endpoint string,
	body io.Reader,
	contentType ContentType,
	headers map[string]string,
) (
	io.Reader,
	error,
) {
	// #nosec G404
	log := s.log.With().Str("id", fmt.Sprintf("%02x", rand.Int31())).Str("address", s.address).Str("endpoint", endpoint).Logger()
	if e := log.Trace(); e.Enabled() {
		bodyBytes, err := io.ReadAll(body)
		if err != nil {
			return nil, errors.New("failed to read request body")
		}
		body = bytes.NewReader(bodyBytes)

		e.Str("body", string(bodyBytes)).Msg("POST request")
	}

	url := urlForCall(s.base, endpoint, "")

	opCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, url.String(), body)
	if err != nil {
		return nil, errors.Join(errors.New("failed to create POST request"), err)
	}
	s.addExtraHeaders(req)
	req.Header.Set("Content-Type", contentType.MediaType())
	// Always take response of POST in JSON, as it's generally small.
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "go-eth2-client/0.19.10")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		go s.CheckConnectionState(ctx)
		s.monitorPostComplete(ctx, url.Path, "failed")

		return nil, errors.Join(errors.New("failed to call POST endpoint"), err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Join(errors.New("failed to read POST response"), err)
	}

	statusFamily := resp.StatusCode / 100
	if statusFamily != 2 {
		log.Trace().Int("status_code", resp.StatusCode).Str("data", string(data)).Msg("POST failed")
		s.monitorPostComplete(ctx, url.Path, "failed")

		return nil, &api.Error{
			Method:     http.MethodPost,
			StatusCode: resp.StatusCode,
			Endpoint:   endpoint,
			Data:       data,
		}
	}

	log.Trace().Str("response", string(data)).Msg("POST response")
	s.monitorPostComplete(ctx, url.Path, "succeeded")

	return bytes.NewReader(data), nil
}

func (s *Service) addExtraHeaders(req *http.Request) {
	for k, v := range s.extraHeaders {
		req.Header.Add(k, v)
	}
}

// responseMetadata returns metadata related to responses.
type responseMetadata struct {
	Version spec.DataVersion `json:"version"`
}

type httpResponse struct {
	statusCode       int
	contentType      ContentType
	headers          map[string]string
	consensusVersion spec.DataVersion
	body             []byte
}

// get sends an HTTP get request and returns the body.
// If the response from the server is a 404 this will return nil for both the reader and the error.
func (s *Service) get(ctx context.Context, endpoint string, query string, opts *api.CommonOpts) (*httpResponse, error) {
	ctx, span := otel.Tracer("attestantio.go-eth2-client.http").Start(ctx, "get")
	defer span.End()

	// #nosec G404
	log := s.log.With().Str("id", fmt.Sprintf("%02x", rand.Int31())).Str("address", s.address).Str("endpoint", endpoint).Logger()
	log.Trace().Msg("GET request")

	url := urlForCall(s.base, endpoint, query)

	timeout := s.timeout
	if opts.Timeout != 0 {
		timeout = opts.Timeout
	}

	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodGet, url.String(), nil)
	if err != nil {
		return nil, errors.Join(errors.New("failed to create GET request"), err)
	}

	s.addExtraHeaders(req)
	if s.enforceJSON {
		// JSON only.
		req.Header.Set("Accept", "application/json")
	} else {
		// Prefer SSZ, JSON if not.
		req.Header.Set("Accept", "application/octet-stream;q=1,application/json;q=0.9")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			// We don't consider context canceled to be a potential connection issue, as the user canceled the context.
		case errors.Is(err, context.DeadlineExceeded):
		// We don't consider context deadline exceeded to be a potential connection issue, as the user selected the deadline.
		case strings.HasSuffix(url.String(), "/node/syncing"):
			// Special case; if we have called the syncing endpoint and it failed then we don't check the connectino status, as
			// that calls the syncing endpoint itself and so we find ourselves in an endless loop.
		default:
			// We consider other errors to be potential connection issues.
			go s.CheckConnectionState(ctx)
		}

		span.RecordError(errors.New("request failed"))
		s.monitorGetComplete(ctx, url.Path, "failed")

		return nil, errors.Join(errors.New("failed to call GET endpoint"), err)
	}
	defer resp.Body.Close()
	log = log.With().Int("status_code", resp.StatusCode).Logger()

	res := &httpResponse{
		statusCode: resp.StatusCode,
	}
	populateHeaders(res, resp)

	if resp.StatusCode == http.StatusNoContent {
		// Nothing returned.  This is not considered an error.
		span.AddEvent("Received empty response")
		log.Trace().Msg("Endpoint returned no content")
		s.monitorGetComplete(ctx, url.Path, "failed")

		return res, nil
	}

	// Although it would be more efficient to keep the body as a Reader, that would
	// require the calling function to be aware that it needs to clode the body
	// once it is done with it.  To avoid that complexity, we read here and store the
	// body as a byte array.
	res.body, err = io.ReadAll(resp.Body)
	if err != nil {
		span.RecordError(err)
		log.Warn().Err(err).Msg("Failed to read body")

		return nil, errors.Join(errors.New("failed to read body"), err)
	}

	if err := populateContentType(res, resp); err != nil {
		// For now, assume that unknown type is JSON.
		log.Debug().Err(err).Msg("Failed to obtain content type; assuming JSON")
		res.contentType = ContentTypeJSON
	}
	span.SetAttributes(attribute.String("content-type", res.contentType.String()))

	if res.contentType == ContentTypeJSON {
		if e := log.Trace(); e.Enabled() {
			e.RawJSON("body", res.body).Msg("Response")
		}
	}

	statusFamily := resp.StatusCode / 100
	if statusFamily != 2 {
		span.SetStatus(codes.Error, fmt.Sprintf("Status code %d", resp.StatusCode))
		trimmedResponse := bytes.ReplaceAll(bytes.ReplaceAll(res.body, []byte{0x0a}, []byte{}), []byte{0x0d}, []byte{})
		log.Debug().Int("status_code", resp.StatusCode).RawJSON("response", trimmedResponse).Msg("GET failed")
		s.monitorGetComplete(ctx, url.Path, "failed")

		return nil, &api.Error{
			Method:     http.MethodGet,
			StatusCode: resp.StatusCode,
			Endpoint:   endpoint,
			Data:       res.body,
		}
	}

	if err := populateConsensusVersion(res, resp); err != nil {
		return nil, errors.Join(errors.New("failed to parse consensus version"), err)
	}

	s.monitorGetComplete(ctx, url.Path, "succeeded")

	return res, nil
}

func populateConsensusVersion(res *httpResponse, resp *http.Response) error {
	res.consensusVersion = spec.DataVersionUnknown
	respConsensusVersions, exists := resp.Header["Eth-Consensus-Version"]
	if !exists {
		// No consensus version supplied in response; obtain it from the body if possible.
		if res.contentType != ContentTypeJSON {
			// Not present here either.  Many responses do not provide this information, so assume
			// this is one of them.
			return nil
		}
		var metadata responseMetadata
		if err := json.Unmarshal(res.body, &metadata); err != nil {
			return errors.Join(errors.New("no consensus version header and failed to parse response"), err)
		}
		res.consensusVersion = metadata.Version

		return nil
	}
	if len(respConsensusVersions) != 1 {
		return fmt.Errorf("malformed consensus version (%d entries)", len(respConsensusVersions))
	}
	if err := res.consensusVersion.UnmarshalJSON([]byte(fmt.Sprintf("%q", respConsensusVersions[0]))); err != nil {
		return errors.Join(errors.New("failed to parse consensus version"), err)
	}

	return nil
}

func populateHeaders(res *httpResponse, resp *http.Response) {
	res.headers = make(map[string]string, len(resp.Header))
	for k, v := range resp.Header {
		res.headers[k] = strings.Join(v, ";")
	}
}

func populateContentType(res *httpResponse, resp *http.Response) error {
	respContentTypes, exists := resp.Header["Content-Type"]
	if !exists {
		return errors.New("no content type supplied in response")
	}
	if len(respContentTypes) != 1 {
		return fmt.Errorf("malformed content type (%d entries)", len(respContentTypes))
	}

	var err error
	res.contentType, err = ParseFromMediaType(respContentTypes[0])
	if err != nil {
		return err
	}

	return nil
}

func metadataFromHeaders(headers map[string]string) map[string]any {
	metadata := make(map[string]any)
	for k, v := range headers {
		metadata[k] = v
	}

	return metadata
}

// urlForCall patches together a URL for a call.
func urlForCall(base *url.URL, endpoint string, query string) *url.URL {
	callURL := *base
	callURL.Path = endpoint
	if callURL.RawQuery == "" {
		callURL.RawQuery = query
	} else if query != "" {
		callURL.RawQuery = fmt.Sprintf("%s&%s", callURL.RawQuery, query)
	}

	return &callURL
}
