package ai

import "testing"

func TestModelImageCapability(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  ImageCapability
	}{
		{"declared with image", []string{"text", "image"}, ImageSupported},
		{"image only", []string{"image"}, ImageSupported},
		{"declared without image", []string{"text"}, ImageUnsupported},
		{"declared with other modalities", []string{"text", "audio"}, ImageUnsupported},
		{"nil input", nil, ImageUnknown},
		{"empty input", []string{}, ImageUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Model{Input: tt.input}).ImageCapability(); got != tt.want {
				t.Errorf("ImageCapability() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestImageUnknownIsZeroValue(t *testing.T) {
	var zero ImageCapability
	if zero != ImageUnknown {
		t.Errorf("zero ImageCapability = %v, want ImageUnknown", zero)
	}
}
