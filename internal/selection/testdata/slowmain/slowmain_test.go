package slowmain

import (
	"os"
	"testing"
	"time"
)

// TestMain takes a second before any test runs.
func TestMain(m *testing.M) {
	time.Sleep(time.Second)
	os.Exit(m.Run())
}

func TestPositive01(*testing.T) { Positive(1) }

func TestPositive02(*testing.T) { Positive(2) }

func TestPositive03(*testing.T) { Positive(3) }

func TestPositive04(*testing.T) { Positive(4) }

func TestPositive05(*testing.T) { Positive(5) }

func TestPositive06(*testing.T) { Positive(6) }

func TestPositive07(*testing.T) { Positive(7) }

func TestPositive08(*testing.T) { Positive(8) }

func TestPositive09(*testing.T) { Positive(9) }

func TestPositive10(*testing.T) { Positive(10) }

func TestPositive11(*testing.T) { Positive(11) }

func TestPositive12(*testing.T) { Positive(12) }

func TestPositive13(*testing.T) { Positive(13) }

func TestPositive14(*testing.T) { Positive(14) }

func TestPositive15(*testing.T) { Positive(15) }

func TestPositive16(*testing.T) { Positive(16) }

func TestPositive17(*testing.T) { Positive(17) }

func TestPositive18(*testing.T) { Positive(18) }

func TestPositive19(*testing.T) { Positive(19) }

func TestPositive20(*testing.T) { Positive(20) }
