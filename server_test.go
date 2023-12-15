package pot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type ServerSuite struct {
	suite.Suite
}

func (s *ServerSuite) TestCanRewrite() {
	cases := []struct {
		caseName         string
		lastModification time.Time
		now              time.Time
		duration         time.Duration
		expected         bool
	}{
		{"same time", time.Now(), time.Now(), time.Second, false},
		{"exact duration", time.Now(), time.Now().Add(time.Second), time.Second, true},
		{"possible rewrite", time.Now(), time.Now().Add(time.Second * 2), time.Second, true},
	}

	for _, c := range cases {
		s.Run(c.caseName, func() {
			s.Equal(c.expected, canRewrite(c.lastModification, c.now, c.duration))
		})
	}
}

func TestServerSuite(t *testing.T) {
	suite.Run(t, new(ServerSuite))
}
