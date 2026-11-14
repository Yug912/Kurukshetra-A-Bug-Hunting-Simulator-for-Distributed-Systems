package engine

import "errors"

// prepareReplay and scoreReplay are implemented in M6.
func (x *run) prepareReplay() error { return errors.New("replay not implemented yet") }

func (x *run) scoreReplay() (float64, error) { return 0, errors.New("replay not implemented yet") }
