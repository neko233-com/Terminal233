package main

import "errors"

// Backups contain indexes instead of service IDs, so importing into a new
// account or team always creates new references within that destination.
func archiveJumpReferences(accounts []cloudAccount) ([]int, []cloudAccount, error) {
	clean := append([]cloudAccount(nil), accounts...)
	hasJumps := false
	for i := range clean {
		if cloudJumpReference(clean[i]) != "" {
			hasJumps = true
		}
		clean[i].JumpID = nil
	}
	if !hasJumps {
		return nil, clean, nil
	}
	ids := make(map[string]int, len(accounts))
	for i, a := range accounts {
		if a.ID == "" || !validCloudJumpID(a.ID) {
			return nil, nil, errors.New("备份中的云主机编号无效，请刷新")
		}
		if _, exists := ids[a.ID]; exists {
			return nil, nil, errors.New("备份中的云主机编号重复")
		}
		ids[a.ID] = i
	}
	parents := make([]int, len(accounts))
	for i, a := range accounts {
		parents[i] = -1
		if id := cloudJumpReference(a); id != "" {
			parent, ok := ids[id]
			if !ok {
				return nil, nil, errors.New("云端跳板不存在，请刷新后再备份")
			}
			parents[i] = parent
		}
	}
	if _, err := archiveJumpOrder(parents, len(accounts)); err != nil {
		return nil, nil, err
	}
	return parents, clean, nil
}

func archiveJumpOrder(parents []int, count int) ([]int, error) {
	if len(parents) != count || count == 0 {
		return nil, errors.New("备份中的跳板路径无效")
	}
	state := make([]uint8, count)
	depths := make([]int, count)
	order := make([]int, 0, count)
	hasJumps := false
	var visit func(int) error
	visit = func(i int) error {
		if i < 0 || i >= count || state[i] == 1 {
			return errors.New("备份中的跳板循环或超过 8 跳")
		}
		if state[i] == 2 {
			return nil
		}
		state[i] = 1
		parent := parents[i]
		if parent != -1 {
			hasJumps = true
			if err := visit(parent); err != nil {
				return err
			}
			depths[i] = depths[parent] + 1
			if depths[i] > maxSSHJumpHosts {
				return errors.New("备份中的跳板循环或超过 8 跳")
			}
		}
		state[i] = 2
		order = append(order, i)
		return nil
	}
	for i := 0; i < count; i++ {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	if !hasJumps {
		return nil, errors.New("备份版本与跳板路径不符")
	}
	return order, nil
}
