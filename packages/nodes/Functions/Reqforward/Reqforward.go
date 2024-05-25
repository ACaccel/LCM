package reqforward

import (
	"fmt"
	i "lcm/packages/interfaces"
	u "lcm/packages/nodes/utils"
	"sort"
)

type InputFormat struct {
	Req      [][]int
	ReqPrime [][]int
	Eij      []struct {
		Idx_i int
		Idx_j int
		Value int
	}
}

type OutputFormat struct {
	Nijk  [][][]int
}

type Property struct {
	
}

var Description = i.NodeDescription{
	NodeType:      "Reqforward",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type Reqforward struct {
	i.FunctionNode
}

func (n *Reqforward) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.GetProperty(n.Config.Property, &property)

	var newInput []InputFormat = make([]InputFormat, len(input))
	for i := 0; i < len(input); i++ {
		err := u.DecodeInput(input[i], &newInput[i])
		if err != nil {
			fmt.Println("Reqforward: Get input error,", err)
		}
	}

	// request forward algorithm
	Req := newInput[0].Req
	ReqPrime := newInput[0].ReqPrime
	Eij := newInput[0].Eij
	var Nijk [][][]int = make([][][]int, len(Req))
	for i := 0; i < len(Req); i++ {
		Nijk[i] = make([][]int, len(Req))
		for j := 0; j < len(Req); j++ {
			Nijk[i][j] = make([]int, len(Req[0]))
		}
	}
	prevCn := make([][]int, len(Req))
	cluster_num := len(Req)
	app_num := len(Req[0])

	for i := 0; i < cluster_num; i++ {
		for j := 0; j < cluster_num; j++ {
			for k := 0; k < app_num; k++ {
				Nijk[i][j][k] = 0
			}
		}
	}

	for i := 0; i < cluster_num; i++ {
		for k := 0; k < app_num; k++ {
			Nijk[i][i][k] = ReqPrime[i][k]
			prevCn[i] = append(prevCn[i], i)
		}
	}

	sort.Slice(Eij, func(i, j int) bool {
		return Eij[i].Value < Eij[j].Value
	})

	for _, eij := range Eij {
		i := eij.Idx_i
		j := eij.Idx_j
		if i != j {
			for k := 0; k < app_num; k++ {
				condition := false
				
				sumi := 0
				for _, alpha := range prevCn[i] {
					sumi += Nijk[alpha][j][k]
				}
				sumj := 0
				for _, beta := range prevCn[j] {
					sumj += Nijk[i][beta][k]
				}

				x := 0
				y := 0
				if (ReqPrime[i][k] + sumj >= Req[i][k]) ||
					(ReqPrime[j][k] <= Req[j][k] + sumi) {
					condition = false
				} else {
					x = ReqPrime[j][k] - Req[j][k] - sumi
					y = Req[i][k] - ReqPrime[i][k] - sumj
					condition = true
				}

				if condition {
					if x < y {
						Nijk[i][j][k] = x
					} else {
						Nijk[i][j][k] = y
					}
				} else {
					Nijk[i][j][k] = 0
				}

				prevCn[i] = append(prevCn[i], j)
				prevCn[j] = append(prevCn[j], i)
			}
		}
	}
	fmt.Println("Nijk: ", Nijk)
	var output OutputFormat = OutputFormat{
		Nijk: Nijk,
	}
	var port []int = []int{0}
	
	var output_map map[string]any = make(map[string]any)
	err := u.EncodeOutput(output, &output_map)
	if err != nil {
		fmt.Println("Reqforward: Encode output error,", err)
	}

	return i.NodeExecResult{
		Output: output_map,
		Ports:  port,
	}
}
