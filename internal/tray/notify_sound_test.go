package tray

import "testing"

func TestSoundSwitch(t *testing.T) {
	if !SoundEnabled() {
		t.Fatal("sound must be on by default")
	}
	SetSoundEnabled(false)
	if SoundEnabled() {
		t.Fatal("sound should be off")
	}
	SetSoundEnabled(true)
	if !SoundEnabled() {
		t.Fatal("sound should be on again")
	}
}
