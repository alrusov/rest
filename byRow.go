package rest

import (
	"bytes"
	"compress/gzip"
	"io"
	"math"
	"net/http"
	"reflect"

	"github.com/alrusov/jsonw"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	ByRow struct {
		Begin []byte // Пишется один раз в начале
		End   []byte // Пишется один раз в конце

		Data []RowData

		RowNum   int            // Количество выданных строк (увеличивается в конце обработки строки)
		IsFinal  bool           // Финал, Tuner еще раз вызывается после завершения выборки, в первом параметре опять последнее значение
		proc     *ProcOptions   //
		tuner    ByRowTuner     //
		withGzip bool           //
		blockNum int            //
		failed   bool           //
		last     any            //
		buf      *bytes.Buffer  //
		writer   io.WriteCloser //
	}

	RowData struct {
		Prefix    []byte // Дополнительный блок цикла, который может возвращаться из ByRowTuner и пишется перед основными данными
		Suffix    []byte // Дополнительный блок цикла, который может возвращаться из ByRowTuner и пишется после основных данных
		Delimiter []byte // Разделитель строк
		Data      any    // Основные данные цикла
	}

	ByRowTuner func(br *ByRow, row any) (err error)
)

const (
	blockBufferSize = 128 * 1024
)

var (
	ByRowDefautlBegin     = []byte{'['}
	ByRowDefautlEnd       = []byte{']'}
	ByRowDefaultDelimiter = []byte{','}
)

//----------------------------------------------------------------------------------------------------------------------------//

func NewByRow(proc *ProcOptions, tuner ByRowTuner) (br *ByRow, err error) {
	br = &ByRow{
		Begin:    ByRowDefautlBegin,
		End:      ByRowDefautlEnd,
		RowNum:   0,
		IsFinal:  false,
		proc:     proc,
		tuner:    tuner,
		withGzip: false,
		blockNum: 0,
		failed:   false,
		buf:      new(bytes.Buffer),
	}

	br.buf.Grow(blockBufferSize)

	br.writer = br
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (br *ByRow) Do() (err error) {
	// При завершении самоликвидируемся
	defer func() {
		if err != nil {
			br.failed = true
		}

		br.Close()
	}()

	// Для упрощения проверки

	if len(br.Begin) == 0 {
		br.Begin = nil
	}
	if len(br.End) == 0 {
		br.End = nil
	}

	// Если нужен gzip, то добавляем упаковку
	if stdhttp.UseGzip(br.proc.R, math.MaxInt, br.proc.ExtraHeaders) {
		br.writer = gzip.NewWriter(br)
		br.withGzip = true
	}

	rows := br.proc.DBqueryRows

	srcTp := br.proc.responseSouceType()

	// Обработка

	for {
		r := reflect.New(srcTp).Interface()
		exists := rows.Next()
		if !exists {
			err = rows.Err()
			if err != nil {
				// Ошибка
				return
			}

			// Выборка завершена
			if br.IsFinal || br.tuner == nil {
				// Финал отработали или нет тюнера
				break
			}

			// Финализируем
			br.IsFinal = true
			r = br.last

		} else {
			// Читаем строку
			err = rows.StructScan(r)
			if err != nil {
				return
			}

			br.last = r
		}

		if br.tuner == nil {
			br.Data = []RowData{
				{
					Prefix:    nil,
					Suffix:    nil,
					Delimiter: ByRowDefaultDelimiter,
					Data:      r,
				},
			}
		} else {
			// Кастомное преобразование
			err = br.tuner(br, r)
			if err != nil {
				return
			}
		}

		if len(br.Data) == 0 {
			// Писать нечего
			continue
		}

		for i, rd := range br.Data {
			if i > 0 || br.RowNum > 0 {
				// Пишем разделитель
				if rd.Delimiter != nil {
					_, err = br.writer.Write(rd.Delimiter)
					if err != nil {
						return
					}
				}
			} else if br.Begin != nil {
				// Пишем начало результата
				_, err = br.writer.Write(br.Begin)
				if err != nil {
					return
				}
			}

			if rd.Prefix != nil {
				// Пишем префикс
				_, err = br.writer.Write(rd.Prefix)
				if err != nil {
					return
				}
			}

			if !misc.IsNil(rd.Data) {
				// Маршалим данные в json
				var j []byte
				j, err = jsonw.Marshal(rd.Data)
				if err != nil {
					return
				}

				// Пишем данные
				_, err = br.writer.Write(j)
				if err != nil {
					return
				}
			}

			if rd.Suffix != nil {
				// Пишем суффикс
				_, err = br.writer.Write(rd.Suffix)
				if err != nil {
					return
				}
			}
		}

		br.Data = nil
		br.RowNum++
	}

	if br.RowNum != 0 && br.End != nil {
		// Пишем конец результата
		_, err = br.writer.Write(br.End)
		if err != nil {
			return
		}
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (br *ByRow) Write(p []byte) (n int, err error) {
	if br.failed {
		return
	}

	ln := len(p)
	if ln == 0 {
		return
	}

	for idx := 0; ln > 0; {
		av := br.buf.Available()
		if av == 0 {
			err = br.Flush()
			if err != nil {
				return
			}
			continue
		}

		if av >= ln {
			av = ln
		}

		var wr int
		wr, err = br.buf.Write(p[idx : idx+av])
		n += wr
		if err != nil {
			return
		}

		idx += wr
		ln -= wr
	}

	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (br *ByRow) Close() (err error) {
	msgs := misc.NewMessages()

	if br.writer != br {
		err = br.writer.Close()
		if err != nil {
			msgs.AddError(err)
		}
	}

	if br.RowNum != 0 {
		err = br.Flush()
		if err != nil {
			msgs.AddError(err)
		}
	} else if !br.failed {
		br.proc.W.WriteHeader(http.StatusNoContent)
	}

	err = msgs.Error()
	return
}

//----------------------------------------------------------------------------------------------------------------------------//

func (br *ByRow) Flush() (err error) {
	if br.buf.Len() == 0 {
		return
	}

	w := br.proc.W

	if br.blockNum == 0 {
		err = stdhttp.WriteContentHeader(w, stdhttp.ContentTypeJSON)
		if err != nil {
			return
		}

		br.proc.ExtraHeaders["X-Content-Type-Options"] = "nosniff"
		if br.withGzip {
			br.proc.ExtraHeaders["Content-Encoding"] = "gzip"
		}

		for n, v := range br.proc.ExtraHeaders {
			w.Header().Set(n, v)
		}

		w.WriteHeader(http.StatusOK)
	}

	_, err = w.Write(br.buf.Bytes())
	if err != nil {
		return
	}

	br.buf.Reset()
	br.blockNum++
	return
}

//----------------------------------------------------------------------------------------------------------------------------//
