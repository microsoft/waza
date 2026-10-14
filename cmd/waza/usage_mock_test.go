package main

import (
	"errors"

	"go.uber.org/mock/gomock"
)

func newSessionMock(ctrl *gomock.Controller) *MockCopilotSession {
	session := NewMockCopilotSession(ctrl)
	session.EXPECT().UsageMetrics(gomock.Any()).Return(nil, errors.New("legacy runtime")).AnyTimes()
	session.EXPECT().ShutdownUsage(gomock.Any()).Return(nil, nil).AnyTimes()
	return session
}
