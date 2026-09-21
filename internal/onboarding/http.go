package onboarding

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func (m Manager) EnrollHTTPHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /enroll/health", func(w http.ResponseWriter, r *http.Request) {
		writeEnrollJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"protocol": "tcp_tls_v1",
		})
	})
	mux.HandleFunc("POST /enroll/request", func(w http.ResponseWriter, r *http.Request) {
		var req EnrollHTTPRequest
		if err := decodeEnrollJSON(r, &req); err != nil {
			writeEnrollError(w, err)
			return
		}
		result, err := m.HandleEnroll(EnrollRequest{
			MACAddress:  req.MACAddress,
			DisplayName: req.DisplayName,
			Token:       req.Token,
			Code:        req.Code,
			NodeName:    req.NodeName,
			CSRPEM:      []byte(req.CSRPEM),
			SourceAddr:  r.RemoteAddr,
		})
		if err != nil {
			writeEnrollError(w, err)
			return
		}
		writeEnrollJSON(w, http.StatusOK, EnrollHTTPResponse{
			OK:      true,
			CAPEM:   string(result.CAPEM),
			CertPEM: string(result.CertPEM),
			Config:  result.Config,
		})
	})
	return mux
}

func decodeEnrollJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return errors.New("missing request body")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func writeEnrollError(w http.ResponseWriter, err error) {
	writeEnrollJSON(w, http.StatusBadRequest, EnrollHTTPResponse{
		OK:    false,
		Error: err.Error(),
	})
}

func writeEnrollJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
