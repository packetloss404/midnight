package router

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "update Traefik renderer golden fixtures")

type goldenCase struct {
	name  string
	input RouteRenderInput
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{
			name: "minimal_single_route",
			input: RouteRenderInput{
				Routes: []RouteInputV2{{
					RouteInput: RouteInput{
						Name:       "app",
						Host:       "app.example.test",
						ServiceURL: "http://10.0.0.1:8080",
					},
				}},
			},
		},
		{
			name: "compress_and_redirect",
			input: RouteRenderInput{
				Routes: []RouteInputV2{{
					RouteInput: RouteInput{
						Name:        "api",
						Host:        "api.example.test",
						ServiceURL:  "http://10.0.0.2:8080",
						TLS:         true,
						Middlewares: []string{"http-to-https", "gzip"},
					},
				}},
				Middlewares: []MiddlewareDefinition{
					{
						Name: "gzip",
						Kind: MiddlewareKindCompress,
						Compress: &CompressMiddleware{
							MinResponseBytes:     1024,
							ExcludedContentTypes: []string{"image/png", "application/octet-stream"},
						},
					},
					{
						Name: "http-to-https",
						Kind: MiddlewareKindRedirectScheme,
						RedirectScheme: &RedirectSchemeMiddleware{
							Scheme:    "https",
							Permanent: true,
							Port:      "443",
						},
					},
				},
			},
		},
		{
			name: "headers_with_hsts",
			input: RouteRenderInput{
				Routes: []RouteInputV2{{
					RouteInput: RouteInput{
						Name:        "secure",
						Host:        "secure.example.test",
						ServiceURL:  "http://10.0.0.3:8080",
						TLS:         true,
						Middlewares: []string{"secure-headers"},
					},
				}},
				Middlewares: []MiddlewareDefinition{{
					Name: "secure-headers",
					Kind: MiddlewareKindHeaders,
					Headers: &HeadersMiddleware{
						HSTSSeconds:           31536000,
						HSTSIncludeSubdomains: true,
						HSTSPreload:           true,
						CustomRequestHeaders: map[string]string{
							"X-Forwarded-Proto": "https",
						},
						CustomResponseHeaders: map[string]string{
							"X-Frame-Options": "DENY",
							"Referrer-Policy": "no-referrer",
							"X-Content-Type":  "nosniff",
						},
					},
				}},
			},
		},
		{
			name: "tls_options_and_mtls",
			input: RouteRenderInput{
				Routes: []RouteInputV2{{
					RouteInput: RouteInput{
						Name:         "internal",
						Host:         "internal.example.test",
						ServiceURL:   "http://10.0.0.4:8080",
						TLS:          true,
						CertResolver: "letsencrypt",
					},
					TLSOptionName: "modern-tls",
					MTLSHint:      "operator-mtls-required",
				}},
				TLSOptions: []TLSOptions{{
					Name:                  "modern-tls",
					MinVersion:            "VersionTLS13",
					CipherSuites:          []string{"TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384"},
					ClientAuthRequireMTLS: true,
					ClientCAFiles:         []string{"/etc/midnight/mtls/ca.pem"},
				}},
			},
		},
		{
			name: "multi_route_full",
			input: RouteRenderInput{
				Routes: []RouteInputV2{
					{
						RouteInput: RouteInput{
							Name:         "alpha",
							Host:         "alpha.example.test",
							ServiceURL:   "http://10.0.0.5:8080",
							TLS:          true,
							CertResolver: "letsencrypt",
							Middlewares:  []string{"gzip", "secure-headers"},
						},
						TLSOptionName: "modern-tls",
					},
					{
						RouteInput: RouteInput{
							Name:        "bravo",
							Host:        "bravo.example.test",
							ServiceURL:  "http://10.0.0.6:8081",
							Middlewares: []string{"http-to-https"},
						},
					},
				},
				Middlewares: []MiddlewareDefinition{
					{Name: "gzip", Kind: MiddlewareKindCompress, Compress: &CompressMiddleware{MinResponseBytes: 1500}},
					{Name: "http-to-https", Kind: MiddlewareKindRedirectScheme, RedirectScheme: &RedirectSchemeMiddleware{Scheme: "https", Permanent: true}},
					{Name: "secure-headers", Kind: MiddlewareKindHeaders, Headers: &HeadersMiddleware{HSTSSeconds: 63072000, HSTSIncludeSubdomains: true}},
				},
				TLSOptions: []TLSOptions{{
					Name:         "modern-tls",
					MinVersion:   "VersionTLS12",
					CipherSuites: []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
				}},
			},
		},
	}
}

func TestRenderTraefikDynamicV2GoldenFiles(t *testing.T) {
	for _, tc := range goldenCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := RenderTraefikDynamicV2(tc.input)
			if err != nil {
				t.Fatalf("RenderTraefikDynamicV2(%s) error: %v", tc.name, err)
			}

			path := filepath.Join("testdata", tc.name+".yaml")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update-golden to create)", path, err)
			}
			wantNormalized := strings.ReplaceAll(string(want), "\r\n", "\n")
			if rendered != wantNormalized {
				t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", tc.name, wantNormalized, rendered)
			}
		})
	}
}

func TestRenderTraefikDynamicV2IsDeterministic(t *testing.T) {
	cases := goldenCases()
	for _, tc := range cases {
		first, err := RenderTraefikDynamicV2(tc.input)
		if err != nil {
			t.Fatalf("first render: %v", err)
		}
		second, err := RenderTraefikDynamicV2(tc.input)
		if err != nil {
			t.Fatalf("second render: %v", err)
		}
		if first != second {
			t.Fatalf("renderer is not deterministic for %s", tc.name)
		}
	}
}

func TestRenderTraefikDynamicV2RejectsUnknownReferences(t *testing.T) {
	_, err := RenderTraefikDynamicV2(RouteRenderInput{
		Routes: []RouteInputV2{{
			RouteInput: RouteInput{Name: "x", Host: "x.example.test", ServiceURL: "http://1.1.1.1:1"},
		}, {
			RouteInput: RouteInput{Name: "y", Host: "y.example.test", ServiceURL: "http://1.1.1.1:1", Middlewares: []string{"missing"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected unknown middleware error, got %v", err)
	}

	_, err = RenderTraefikDynamicV2(RouteRenderInput{
		Routes: []RouteInputV2{{
			RouteInput:    RouteInput{Name: "x", Host: "x.example.test", ServiceURL: "http://1.1.1.1:1"},
			TLSOptionName: "missing",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "TLS option") {
		t.Fatalf("expected unknown TLS option error, got %v", err)
	}
}

func TestRenderTraefikDynamicV2RejectsBadInputs(t *testing.T) {
	_, err := RenderTraefikDynamicV2(RouteRenderInput{
		Routes: []RouteInputV2{
			{RouteInput: RouteInput{Name: "dup", Host: "dup.example.test", ServiceURL: "http://1.1.1.1:1"}},
			{RouteInput: RouteInput{Name: "dup", Host: "dup.example.test", ServiceURL: "http://1.1.1.1:1"}},
		},
	})
	if err == nil {
		t.Fatal("expected duplicate-route error")
	}

	_, err = RenderTraefikDynamicV2(RouteRenderInput{
		Middlewares: []MiddlewareDefinition{{Name: "", Kind: MiddlewareKindCompress, Compress: &CompressMiddleware{}}},
	})
	if err == nil {
		t.Fatal("expected validation error for empty middleware name")
	}
}
