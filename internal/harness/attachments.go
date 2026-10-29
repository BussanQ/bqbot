package harness

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"bqbot/internal/driver"
	_ "golang.org/x/image/webp"
)

// Bound encoded/decoded size and pixel count before any transcript or driver
// sees an attachment. MIME must agree with the actual image, not its filename.
func validateAttachments(attachments []driver.Attachment) error {
	if len(attachments) > 8 {
		return errors.New("at most 8 images are allowed")
	}
	total := 0
	for _, a := range attachments {
		if len(a.Data) > 8<<20 {
			return errors.New("image exceeds 6 MiB")
		}
		b, err := base64.StdEncoding.DecodeString(a.Data)
		if err != nil {
			return fmt.Errorf("image %q: invalid base64", a.Name)
		}
		total += len(b)
		if len(b) > 6<<20 || total > 12<<20 {
			return errors.New("images exceed size limit")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("image %q: invalid or unsupported image", a.Name)
		}
		mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif", "webp": "image/webp"}[format]
		if mime == "" || a.MIME != mime {
			return fmt.Errorf("image %q: MIME does not match image", a.Name)
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
			return errors.New("image dimensions exceed limit")
		}
		if _, _, err := image.Decode(bytes.NewReader(b)); err != nil {
			return fmt.Errorf("image %q: corrupt image", a.Name)
		}
	}
	return nil
}
