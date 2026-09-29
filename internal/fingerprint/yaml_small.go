//go:build lanscape_small

package fingerprint

import "errors"

func unmarshalYAML([]byte, any) error {
	return errors.New("this build reads signature files in JSON only")
}
