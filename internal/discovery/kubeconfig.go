package discovery

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

type kubeconfig struct {
	CurrentContext string `json:"current-context" yaml:"current-context"`
	Contexts       []struct {
		Name    string `json:"name"`
		Context struct {
			Cluster string `json:"cluster"`
			User    string `json:"user"`
		} `json:"context"`
	} `json:"contexts"`
	Clusters []struct {
		Name    string `json:"name"`
		Cluster struct {
			Server   string `json:"server"`
			CAData   string `json:"certificate-authority-data" yaml:"certificate-authority-data"`
			CAFile   string `json:"certificate-authority" yaml:"certificate-authority"`
			Insecure bool   `json:"insecure-skip-tls-verify" yaml:"insecure-skip-tls-verify"`
		} `json:"cluster"`
	} `json:"clusters"`
	Users []struct {
		Name string `json:"name"`
		User struct {
			Token    string `json:"token"`
			CertData string `json:"client-certificate-data" yaml:"client-certificate-data"`
			KeyData  string `json:"client-key-data" yaml:"client-key-data"`
		} `json:"user"`
	} `json:"users"`
}

func kubeconfigClient(path string) (*k8sClient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("k8s: %w", err)
	}
	var kc kubeconfig
	if err := unmarshalKubeconfig(b, &kc); err != nil {
		return nil, fmt.Errorf("k8s: kubeconfig: %w", err)
	}
	var clusterName, userName string
	for _, c := range kc.Contexts {
		if c.Name == kc.CurrentContext || kc.CurrentContext == "" {
			clusterName, userName = c.Context.Cluster, c.Context.User
			break
		}
	}
	cl := &k8sClient{}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	for _, c := range kc.Clusters {
		if c.Name != clusterName {
			continue
		}
		cl.base = strings.TrimRight(c.Cluster.Server, "/")
		tc.InsecureSkipVerify = c.Cluster.Insecure //nolint:gosec // honours the kubeconfig setting
		var ca []byte
		if c.Cluster.CAData != "" {
			ca, _ = base64.StdEncoding.DecodeString(c.Cluster.CAData)
		} else if c.Cluster.CAFile != "" {
			ca, _ = os.ReadFile(c.Cluster.CAFile)
		}
		if len(ca) > 0 {
			tc.RootCAs = x509.NewCertPool()
			tc.RootCAs.AppendCertsFromPEM(ca)
		}
	}
	for _, u := range kc.Users {
		if u.Name != userName {
			continue
		}
		cl.token = u.User.Token
		if u.User.CertData != "" {
			cert, _ := base64.StdEncoding.DecodeString(u.User.CertData)
			key, _ := base64.StdEncoding.DecodeString(u.User.KeyData)
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				return nil, fmt.Errorf("k8s: client certificate: %w", err)
			}
			tc.Certificates = []tls.Certificate{pair}
		}
	}
	if cl.base == "" {
		return nil, errors.New("k8s: kubeconfig has no server for the current context")
	}
	cl.hc = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{TLSClientConfig: tc}}
	return cl, nil
}
