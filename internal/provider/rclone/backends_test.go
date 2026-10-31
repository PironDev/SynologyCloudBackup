package rclone

import (
	"testing"

	"github.com/rclone/rclone/fs"
)

// Ключи полей пакета должны совпадать с опциями rclone, иначе облако молча игнорирует настройку.
func TestDescriptorKeysExistInRclone(t *testing.T) {
	common := map[string]bool{"root": true, "enc_password": true, "split_mb": true}
	for _, d := range Descriptors() {
		ri, err := fs.Find(d.Kind)
		if err != nil {
			t.Fatalf("%s: %v", d.Kind, err)
		}
		known := map[string]bool{}
		for _, o := range ri.Options {
			known[o.Name] = true
		}
		for _, f := range d.Fields {
			if !common[f.Key] && !known[f.Key] {
				t.Errorf("%s: поле %q нет в опциях rclone", d.Kind, f.Key)
			}
		}
	}
}
