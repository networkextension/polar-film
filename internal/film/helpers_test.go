package film

import "testing"

func TestIsAnonymousSpeaker(t *testing.T) {
	anon := []string{"", "spk?", "spk0", "spk12", "spk999"}
	named := []string{"Darcy", "Elizabeth", "Mr. Bennet", "spk", "spkX", "0spk0", "spk0x"}
	for _, k := range anon {
		if !isAnonymousSpeaker(k) {
			t.Errorf("isAnonymousSpeaker(%q)=false, want true", k)
		}
	}
	for _, k := range named {
		if isAnonymousSpeaker(k) {
			t.Errorf("isAnonymousSpeaker(%q)=true, want false", k)
		}
	}
}
