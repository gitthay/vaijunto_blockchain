package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"vaiJunto/blockchain"
	"vaiJunto/utils"
)

var leitor = bufio.NewReader(os.Stdin)

func lerTexto(rotulo string) string {
	fmt.Print(rotulo)
	texto, _ := leitor.ReadString('\n')
	// Remove a quebra de linha (\n ou \r\n) e espaços extras antes e depois
	return strings.TrimSpace(texto)
}

// enviarRequisicaoTCP envia uma requisição ao servidor e detecta quedas de conexão (EOF / reset).
// Se o servidor tiver caído, avisa claramente ao usuário e tenta reconectar automaticamente
// caso o servidor já tenha voltado ao ar.
func enviarRequisicaoTCP(conexao *net.Conn, serverAddr, email, senha string, req utils.MensagemRequisicao, buf []byte) (utils.MensagemResposta, bool) {
	reqBytes, _ := json.Marshal(req)

	if *conexao != nil {
		_, errW := (*conexao).Write(reqBytes)
		if errW == nil {
			// json.Decoder lê do socket até completar o objeto JSON (respostas grandes chegam fragmentadas)
			var resp utils.MensagemResposta
			if errR := json.NewDecoder(*conexao).Decode(&resp); errR == nil {
				return resp, true
			}
		}
		_ = (*conexao).Close()
		*conexao = nil
	}

	fmt.Printf("\n[ALERTA DE REDE]: A conexão com o servidor (%s) foi perdida! Tentando reconectar...\n", serverAddr)

	novaConn, errDial := net.DialTimeout("tcp", serverAddr, 2*time.Second)
	if errDial != nil {
		fmt.Printf("[SERVIDOR OFFLINE]: O servidor (%s) caiu e continua fora do ar no momento. Aguarde ele voltar e tente novamente.\n", serverAddr)
		return utils.MensagemResposta{}, false
	}

	// Servidor voltou: reautentica automaticamente a sessão do passageiro
	payloadAuth, _ := json.Marshal(map[string]interface{}{
		"email":         email,
		"senha":         senha,
		"chave_publica": utils.ChavePublicaDoUsuario(email),
		"tipo":          utils.TipoPassageiro,
	})
	reqAuth := utils.MensagemRequisicao{
		Acao:    utils.AcaoAutenticar,
		Usuario: email,
		Payload: payloadAuth,
	}
	reqAuthBytes, _ := json.Marshal(reqAuth)
	if _, err := novaConn.Write(reqAuthBytes); err != nil {
		_ = novaConn.Close()
		fmt.Printf("[SERVIDOR OFFLINE]: Falha ao restabelecer sessão com o servidor (%s).\n", serverAddr)
		return utils.MensagemResposta{}, false
	}
	var respAuthReconexao utils.MensagemResposta
	if errAuth := json.NewDecoder(novaConn).Decode(&respAuthReconexao); errAuth != nil {
		_ = novaConn.Close()
		fmt.Printf("[SERVIDOR OFFLINE]: O servidor (%s) não respondeu à reconexão.\n", serverAddr)
		return utils.MensagemResposta{}, false
	}

	if !respAuthReconexao.Sucesso {
		_ = novaConn.Close()
		fmt.Println("[ERRO]:", respAuthReconexao.Mensagem)
		return utils.MensagemResposta{}, false
	}
	// Reenvia a requisição original após reconectar com sucesso
	if _, err := novaConn.Write(reqBytes); err != nil {
		_ = novaConn.Close()
		fmt.Printf("[SERVIDOR OFFLINE]: Perda de conexão ao reenviar requisição para (%s).\n", serverAddr)
		return utils.MensagemResposta{}, false
	}
	var resp utils.MensagemResposta
	if err := json.NewDecoder(novaConn).Decode(&resp); err != nil {
		_ = novaConn.Close()
		fmt.Printf("[SERVIDOR OFFLINE]: Sem resposta do servidor (%s).\n", serverAddr)
		return utils.MensagemResposta{}, false
	}

	*conexao = novaConn
	fmt.Printf("[RECONECTADO]: Conexão restabelecida automaticamente com o servidor (%s)!\n", serverAddr)

	return resp, true
}

// função auxiliar para consultar as reservas do cliente
func buscarReservas(conexao *net.Conn, serverAddr, email, senha string, buf []byte) ([]utils.Reserva, bool) {
	req := utils.MensagemRequisicao{Acao: utils.AcaoListarReservas, Usuario: email}
	resp, ok := enviarRequisicaoTCP(conexao, serverAddr, email, senha, req, buf)
	if !ok {
		return nil, false
	}

	var reservas []utils.Reserva
	_ = json.Unmarshal(resp.Payload, &reservas)
	return reservas, true
}

// exibirDetalhesReserva imprime a reserva e cada um de seus trechos, indicando se algum servidor participante está offline
func exibirDetalhesReserva(indice int, r utils.Reserva) {
	origemGeral := "[Servidor Offline]"
	destinoGeral := "[Servidor Offline]"
	dataHoraStr := "Consultando servidor..."

	if len(r.Itinerario) > 0 {
		if r.Itinerario[0].Origem != "" {
			origemGeral = r.Itinerario[0].Origem
		}
		if r.Itinerario[len(r.Itinerario)-1].Destino != "" {
			destinoGeral = r.Itinerario[len(r.Itinerario)-1].Destino
		}
		if !r.Itinerario[0].HorarioPartida.IsZero() && !r.Itinerario[0].ServidorOffline {
			dataHoraStr = fmt.Sprintf("%s às %s",
				r.Itinerario[0].HorarioPartida.Format("02/01/2006"),
				r.Itinerario[0].HorarioPartida.Format("15:04"))
		} else if r.Itinerario[0].ServidorOffline {
			dataHoraStr = "[Servidor do 1º trecho OFFLINE]"
		}
	}

	fmt.Printf("\n[%d] ID Reserva: %s | Rota: %s -> %s\n", indice, r.ID, origemGeral, destinoGeral)
	if r.Status != "" {
		fmt.Println("    Status:", r.Status)
	}
	fmt.Printf("    Partida: %s | Valor Total: T$ %.2f | Trechos: %d\n", dataHoraStr, r.ValorTotal, len(r.Itinerario))

	for j, t := range r.Itinerario {
		emp := t.EmpresaID
		if emp == "" {
			emp = "local"
		}
		if t.ServidorOffline {
			fmt.Printf("      Trecho %d [%s]: (Carona ID: %s | Servidor: %s) -> [SERVIDOR OFFLINE - Detalhes indisponíveis no momento]\n",
				j+1, emp, t.CaronaID, t.ServidorURL)
		} else {
			fmt.Printf("      Trecho %d [%s]: %s -> %s (Carona ID: %s | Motorista: %s | Partida: %s | Chegada: %s | T$ %.2f)\n",
				j+1, emp, t.Origem, t.Destino, t.CaronaID, t.MotoristaID,
				t.HorarioPartida.Format("15:04"), t.HorarioChegada.Format("15:04"), t.Preco)
		}
	}
}

func main() {
	// Flag para aceitar IP dinâmico na execução do terminal
	serverAddr := flag.String("server", "localhost:8080", "Endereço do servidor TCP (ex: 192.168.1.15:8080)")
	flag.Parse()

	// Conecta ao endereço configurado
	conexao, err := net.Dial("tcp", *serverAddr)
	if err != nil {
		log.Fatalln("Erro ao conectar ao servidor:", err)
	}
	defer func() {
		if conexao != nil {
			_ = conexao.Close()
		}
	}()

	buf := make([]byte, 16384)

	fmt.Println("==========================================")
	fmt.Println("        VAIJUNTO - PASSAGEIRO             ")
	fmt.Println("==========================================")

	//AUTENTICAÇÃO
	var email, senha string
	var carteira *utils.Carteira
	for {
		fmt.Println("\n1. Entrar\n2. Criar conta\n0. Sair")
		opcao := lerTexto("Escolha: ")
		if opcao == "0" {
			return
		}
		if opcao != "1" && opcao != "2" {
			continue
		}
		email = lerTexto("Digite seu Email: ")
		senha = lerTexto("Digite sua Senha: ")
		if email == "" || senha == "" {
			fmt.Println("Informe e-mail e senha.")
			continue
		}
		acao := utils.AcaoAutenticar
		var errCarteira error
		if opcao == "2" {
			if senha != lerTexto("Confirme sua Senha: ") {
				fmt.Println("Senhas diferentes.")
				continue
			}
			acao = utils.AcaoCadastrar
			carteira, errCarteira = utils.PrepararCarteiraCadastro(email)
		} else {
			carteira, errCarteira = utils.CarregarCarteiraExistente(email)
		}
		if errCarteira != nil {
			fmt.Printf("[ERRO] Carteira local indisponível: %v. No login, restaure a carteira original; nenhuma chave será criada.\n", errCarteira)
			continue
		}
		payload, _ := json.Marshal(map[string]interface{}{"email": email, "senha": senha, "chave_publica": carteira.ChavePublica, "tipo": utils.TipoPassageiro})
		if err := json.NewEncoder(conexao).Encode(utils.MensagemRequisicao{Acao: acao, Usuario: email, Payload: payload}); err != nil {
			log.Fatal(err)
		}
		var resp utils.MensagemResposta
		if err := json.NewDecoder(conexao).Decode(&resp); err != nil {
			log.Fatal(err)
		}
		if !resp.Sucesso {
			fmt.Println("[ERRO]:", resp.Mensagem)
			continue
		}
		fmt.Println("[SUCESSO]:", resp.Mensagem)
		break
	}

	// MENU PRINCIPAL
	for {
		fmt.Println("\n------------------------------------------")
		fmt.Println("1. Buscar/Reservar Caronas")
		fmt.Println("2. Minhas Reservas")
		fmt.Println("3. Cancelar Reserva")
		fmt.Println("4. Carteira de Tokens")
		fmt.Println("5. Concluir viagem")
		fmt.Println("0. Sair")
		fmt.Println("------------------------------------------")

		opcao := lerTexto("Escolha uma opção: ")

		switch opcao {
		case "1":
			fmt.Println("\n--- BUSCAR ITINERÁRIOS (Digite 0 em qualquer campo para voltar) ---")
			origem := lerTexto("Origem: ")
			if origem == "0" {
				continue
			}

			destino := lerTexto("Destino: ")
			if destino == "0" {
				continue
			}

			// Leitura e validação da data
			var dataFiltro utils.Data
			cancelarBusca := false
			for {
				dataStr := lerTexto("Data da viagem (DD/MM/AAAA): ")
				if dataStr == "0" {
					cancelarBusca = true
					break
				}
				partes := strings.Split(dataStr, "/")
				if len(partes) != 3 {
					fmt.Println("[ERRO]: Formato inválido. Use DD/MM/AAAA. Tente novamente.")
					continue
				}
				d, errD := strconv.Atoi(partes[0])
				m, errM := strconv.Atoi(partes[1])
				a, errA := strconv.Atoi(partes[2])
				if errD != nil || errM != nil || errA != nil || d < 1 || d > 31 || m < 1 || m > 12 {
					fmt.Println("[ERRO]: Data inválida. Tente novamente.")
					continue
				}
				dataFiltro = utils.NovaData(d, m, a)
				break
			}
			if cancelarBusca {
				continue
			}

			filtro := struct {
				Origem  string     `json:"origem"`
				Destino string     `json:"destino"`
				Data    utils.Data `json:"data"`
			}{
				Origem:  origem,
				Destino: destino,
				Data:    dataFiltro,
			}

			payload, _ := json.Marshal(filtro)
			req := utils.MensagemRequisicao{Acao: utils.AcaoBuscarItinerario, Usuario: email, Payload: payload}
			resp, ok := enviarRequisicaoTCP(&conexao, *serverAddr, email, senha, req, buf)
			if !ok {
				continue
			}

			var itinerariosEncontrados []utils.Itinerario
			_ = json.Unmarshal(resp.Payload, &itinerariosEncontrados)

			fmt.Println("\n==========================================")
			fmt.Printf("   %s\n", resp.Mensagem)
			fmt.Println("==========================================")

			if len(itinerariosEncontrados) == 0 {
				fmt.Println("Nenhum itinerário encontrado para a rota e data especificadas.")
				continue
			}

			for i, itin := range itinerariosEncontrados {
				fmt.Printf("\nOPÇÃO [%d] - Preço Total: T$ %.2f\n", i+1, itin.PrecoTotal)
				for j, t := range itin.Trechos {
					emp := t.EmpresaID
					if emp == "" {
						emp = "local"
					}
					fmt.Printf("  Trecho %d [%s]: %s -> %s | Partida: %s | Chegada: %s (Carona ID: %s | Motorista: %s)\n",
						j+1, emp, t.Origem, t.Destino, t.HorarioPartida.Format("15:04"), t.HorarioChegada.Format("15:04"), t.CaronaID, t.MotoristaID)
				}
			}

			// Pergunta se deseja reservar ou voltar (repete se digitar algo inválido)
			fmt.Println("\n------------------------------------------")
			var desejaReservar string
			for {
				desejaReservar = strings.ToLower(lerTexto("Deseja reservar algum desses itinerários? (s/n): "))
				if desejaReservar == "s" || desejaReservar == "n" {
					break
				}
				fmt.Println("[ERRO]: Digite apenas 's' para sim ou 'n' para não.")
			}

			if desejaReservar == "n" {
				continue
			}

			input := lerTexto("Digite o número da OPÇÃO desejada (ou 0 para cancelar): ")
			num, err := strconv.Atoi(input)
			if err != nil || num <= 0 || num > len(itinerariosEncontrados) {
				fmt.Println("Reserva não realizada.")
				continue
			}

			itinerarioEscolhido := itinerariosEncontrados[num-1]
			rawCotacao, _ := json.Marshal(itinerarioEscolhido)
			cot, ok := enviarRequisicaoTCP(&conexao, *serverAddr, email, senha, utils.MensagemRequisicao{Acao: utils.AcaoCotarReserva, Usuario: email, Payload: rawCotacao}, buf)
			if !ok {
				continue
			}
			if !cot.Sucesso {
				fmt.Println("[ERRO]", cot.Mensagem)
				continue
			}
			var c utils.CotacaoReserva
			if json.Unmarshal(cot.Payload, &c) != nil {
				fmt.Println("Cotação inválida")
				continue
			}
			fmt.Printf("Saldo: %d tokens | Bloqueio: %d tokens\n", c.Saldo, c.Escrow.ValorTotal)
			if strings.ToLower(lerTexto("Confirmar bloqueio de tokens? (s/n): ")) != "s" {
				continue
			}
			tx, err := blockchain.NovaTransacaoCriarEscrow(carteira, c.Escrow, c.Nonce, time.Now().Unix())
			if err != nil {
				fmt.Println(err)
				continue
			}
			payloadReserva, _ := json.Marshal(utils.ReservaComTokens{Itinerario: c.Itinerario, Transacao: tx})

			reqReserva := utils.MensagemRequisicao{Acao: utils.AcaoReservarTrecho, Usuario: email, Payload: payloadReserva}
			respReserva, ok := enviarRequisicaoTCP(&conexao, *serverAddr, email, senha, reqReserva, buf)
			if !ok {
				continue
			}

			fmt.Println("\n>", respReserva.Mensagem)

		case "2":
			reservas, ok := buscarReservas(&conexao, *serverAddr, email, senha, buf)
			if !ok {
				continue
			}
			fmt.Println("\n--- MINHAS RESERVAS (CONSULTA EM TEMPO REAL AO CONSÓRCIO) ---")
			if len(reservas) == 0 {
				fmt.Println("Você não possui reservas ativas.")
				continue
			}
			for i, r := range reservas {
				exibirDetalhesReserva(i+1, r)
			}

		case "3", "5":
			reservas, ok := buscarReservas(&conexao, *serverAddr, email, senha, buf)
			if !ok {
				continue
			}
			for i, r := range reservas {
				exibirDetalhesReserva(i+1, r)
			}
			id := lerTexto("ID da reserva (inclusive escrow pendente; 0 volta): ")
			if id == "0" || id == "" {
				continue
			}
			if strings.ToLower(lerTexto("Confirma a operação? (s/n): ")) != "s" {
				continue
			}
			if opcao == "5" && strings.ToLower(lerTexto("Confirma que chegou ao destino final? (s/n): ")) != "s" {
				continue
			}
			resp, ok := enviarRequisicaoTCP(&conexao, *serverAddr, email, senha, utils.MensagemRequisicao{Acao: utils.AcaoSaldoTokens, Usuario: email}, buf)
			if !ok || !resp.Sucesso {
				fmt.Println(resp.Mensagem)
				continue
			}
			var saldo utils.RespostaSaldo
			if json.Unmarshal(resp.Payload, &saldo) != nil {
				continue
			}
			var tx blockchain.Transacao
			var err error
			acao := utils.AcaoCancelarReserva
			if opcao == "5" {
				acao = utils.AcaoConcluirViagem
				tx, err = blockchain.NovaTransacaoConcluirViagem(carteira, id, saldo.ProximoNonce, time.Now().Unix())
			} else {
				tx, err = blockchain.NovaTransacaoCancelarEscrow(carteira, id, saldo.ProximoNonce, time.Now().Unix())
			}
			if err != nil {
				fmt.Println(err)
				continue
			}
			payload, _ := json.Marshal(tx)
			resp, ok = enviarRequisicaoTCP(&conexao, *serverAddr, email, senha, utils.MensagemRequisicao{Acao: acao, Usuario: email, Payload: payload}, buf)
			if ok {
				fmt.Println(resp.Mensagem)
			}

		case "4":
			utils.MenuTokens(email, carteira, func(r utils.MensagemRequisicao) (utils.MensagemResposta, bool) {
				return enviarRequisicaoTCP(&conexao, *serverAddr, email, senha, r, buf)
			}, lerTexto)
		case "0":
			fmt.Println("Saindo...")
			return
		}
	}
}
