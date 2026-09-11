package api

func (s Server) exclusive(action func() error) error {
	release, err := s.Store.BeginOperation("")
	if err != nil {
		return err
	}
	defer release()
	return action()
}
