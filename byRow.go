package rest

import (
	"bytes"
	"compress/gzip"
	"io"
	"math"
	"reflect"

	"github.com/alrusov/jsonw"
	"github.com/alrusov/misc"
	"github.com/alrusov/stdhttp"
)

//----------------------------------------------------------------------------------------------------------------------------//

type (
	ByRow struct {
		Begin     []byte         // Пишется один раз в начале
		End       []byte         // Пишется один раз в конце
		Delimiter []byte         // Разделитель строк
		Prefix    []byte         // Дополнительный блок цикла, который может возвращаться из ByRowTuner и пишется один раз перед основными данными
		Data      []any          // Основные данные цикла
		Suffix    []byte         // Дополнительный блок цикла, который может возвращаться из ByRowTuner и пишется один раз после основных данных
		RowNum    int            // Количество выданных строк (увеличивается в конце обработки строки)
		IsFinal   bool           // Финал, Tuner еще раз вызывается после завершения выборки, в первом параметре опять последнее значение
		proc      *ProcOptions   //
		tuner     ByRowTuner     //
		withGzip  bool           //
		blockNum  int            //
		failed    bool           //
		last      any            //
		buf       *bytes.Buffer  //
		writer    io.WriteCloser //
	}

	ByRowTuner func(br *ByRow, row any) (err error)
)

const (
	blockBufferSize = 128 * 1024
)

//----------------------------------------------------------------------------------------------------------------------------//

func NewByRow(proc *ProcOptions, tuner ByRowTuner) (br *ByRow, err error) {
	br = &ByRow{
		Begin:     []byte{'['},
		End:       []byte{']'},
		Delimiter: []byte{','},
		RowNum:    0,
		IsFinal:   false,
		proc:      proc,
		tuner:     tuner,
		withGzip:  false,
		blockNum:  0,
		failed:    false,
		buf:       new(bytes.Buffer),
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
	if len(br.Delimiter) == 0 {
		br.Delimiter = nil
	}

	// Если нужен gzip, то добавляем упаковку
	if stdhttp.UseGzip(br.proc.R, math.MaxInt, br.proc.ExtraHeaders) {
		br.writer = gzip.NewWriter(br)
		br.withGzip = true
	}

	rows := br.proc.DBqueryRows

	srcTp := br.proc.responseSouceType()
	r := reflect.New(srcTp).Interface()

	// Обработка

	for {
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
			br.Data = []any{r}
		} else {
			// Кастомное преобразование
			err = br.tuner(br, r)
			if err != nil {
				return
			}
		}

		if len(br.Data) == 0 && br.Prefix == nil && br.Suffix == nil {
			// Писать нечего
			continue
		}

		if br.RowNum > 0 {
			// Пишем разделитель
			if br.Delimiter != nil {
				_, err = br.writer.Write(br.Delimiter)
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

		if br.Prefix != nil {
			// Пишем префикс
			_, err = br.writer.Write(br.Prefix)
			if err != nil {
				return
			}
			br.Prefix = nil
		}

		if len(br.Data) > 0 {
			// Маршалим данные в json
			var j []byte
			for _, r := range br.Data {
				j, err = jsonw.Marshal(r)
				if err != nil {
					return
				}
			}

			br.Data = nil

			// Пишем данные
			_, err = br.writer.Write(j)
			if err != nil {
				return
			}
		}

		if br.Suffix != nil {
			// Пишем суффикс
			_, err = br.writer.Write(br.Suffix)
			if err != nil {
				return
			}
			br.Suffix = nil
		}

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

	err = br.Flush()
	if err != nil {
		msgs.AddError(err)
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
