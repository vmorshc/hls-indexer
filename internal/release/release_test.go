package release

import "testing"

func TestIDRoundTrip(t *testing.T) {
	tests := []struct {
		id  ID
		str string
	}{
		{ID{Source: "uakino", TitleID: "13059-eyforya-2-sezon", Season: 2, Episode: 3, Voice: "9f86d081"}, "uakino:13059-eyforya-2-sezon:s02e03:9f86d081"},
		{ID{Source: "uakino", TitleID: "312-shrek-2", Voice: "ab12cd34"}, "uakino:312-shrek-2:movie:ab12cd34"},
		{ID{Source: "uakino", TitleID: "1-x", Season: 1, Episode: 1105, Voice: "00000000"}, "uakino:1-x:s01e1105:00000000"},
	}
	for _, tt := range tests {
		if got := tt.id.String(); got != tt.str {
			t.Errorf("String() = %q, want %q", got, tt.str)
		}
		got, err := Parse(tt.str)
		if err != nil || got != tt.id {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", tt.str, got, err, tt.id)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"uakino:312-shrek-2:movie",
		"uakino:312-shrek-2:movie:ab12cd3",
		"uakino:312-shrek-2:movie:AB12CD34",
		"uakino:312-shrek-2:s1e1:ab12cd34",
		"uakino:312-shrek-2:s00e01:ab12cd34",
		"uakino::movie:ab12cd34",
		":312-shrek-2:movie:ab12cd34",
		"uakino:312 shrek:movie:ab12cd34",
		"uakino:312-shrek-2:movie:ab12cd34:x",
		"uakino:1-x:s01e99999999999999999999:ab12cd34",
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q): want error", s)
		}
	}
}

func TestVoiceHash(t *testing.T) {
	// From `printf dniprofilm | shasum -a 256`.
	const want = "0a3a8c0e"
	for _, v := range []string{"DniproFilm", " DniproFilm ", "DNIPROFILM"} {
		if got := VoiceHash(v); got != want {
			t.Errorf("VoiceHash(%q) = %q, want %q", v, got, want)
		}
	}
	if VoiceHash("Gwean &  Maslinka") != VoiceHash("gwean & maslinka") {
		t.Error("whitespace not collapsed")
	}
	if VoiceHash("1+1") == VoiceHash("Cine+") {
		t.Error("different voices share a hash")
	}
	if got := VoiceHash("abc"); got != "ba7816bf" {
		t.Errorf("VoiceHash(abc) = %q, want sha256 prefix ba7816bf", got)
	}
}

func TestGroup(t *testing.T) {
	tests := map[string]string{
		"Gwean & Maslinka":    "GweanMaslinka",
		"У куточку в таверні": "UKutochkuVTaverni",
		"1+1":                 "1Plus1",
		"Cine+":               "CinePlus",
		"ТакТребаПродакшн":    "TakTrebaProdakshn",
		"Мова жестів":         "MovaZhestiv",
		"Новий канал":         "NovyiKanal",
		"FanWoxUA":            "FanWoxUA",
		"Megogo Voice":        "MegogoVoice",
		"Espada studio":       "EspadaStudio",
		"Щедрик Юрій Яна Їжак Єва": "ShchedrykYuriiYanaYizhakYeva",
		"":   "Unknown",
		"«»": "Unknown",
	}
	for voice, want := range tests {
		if got := Group(voice); got != want {
			t.Errorf("Group(%q) = %q, want %q", voice, got, want)
		}
	}
}

func TestResolution(t *testing.T) {
	tests := []struct {
		w, h int
		want string
	}{
		{1920, 1080, "1080p"},
		{1920, 816, "1080p"},
		{1280, 720, "720p"},
		{1280, 536, "720p"},
		{854, 480, "480p"},
		{3840, 2160, "2160p"},
		{1024, 576, "576p"},
		{0, 0, ""},
	}
	for _, tt := range tests {
		if got := Resolution(tt.w, tt.h); got != tt.want {
			t.Errorf("Resolution(%d, %d) = %q, want %q", tt.w, tt.h, got, tt.want)
		}
	}
}

func TestTitle(t *testing.T) {
	tests := []struct {
		in   Name
		want string
	}{
		{Name{Title: "Euphoria", Season: 2, Episode: 3, Resolution: "1080p", Voice: "DniproFilm"}, "Euphoria.S02E03.1080p.WEB-DL.UKR-DniproFilm"},
		{Name{Title: "Shrek 2", Year: 2004, Resolution: "1080p", Voice: "ТакТребаПродакшн"}, "Shrek.2.2004.1080p.WEB-DL.UKR-TakTrebaProdakshn"},
		{Name{Title: "Mission: Impossible - Fallout", Year: 2018, Resolution: "720p", Voice: "1+1"}, "Mission.Impossible.Fallout.2018.720p.WEB-DL.UKR-1Plus1"},
		{Name{Title: "Grey's Anatomy", Season: 1, Episode: 12, Resolution: "1080p", Voice: "x"}, "Greys.Anatomy.S01E12.1080p.WEB-DL.UKR-X"},
		{Name{Title: "Людина-бензопила", Season: 1, Episode: 1, Voice: "Dzuski"}, "Liudyna.benzopyla.S01E01.WEB-DL.UKR-Dzuski"},
		{Name{Title: "Léon", Year: 1994, Resolution: "1080p", Voice: "Dzuski"}, "Léon.1994.1080p.WEB-DL.UKR-Dzuski"},
	}
	for _, tt := range tests {
		if got := Title(tt.in); got != tt.want {
			t.Errorf("Title(%+v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
