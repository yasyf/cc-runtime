import { useState } from 'react';
import { useAnswer } from '../api';
import type { OpenQuestion, Option } from '../events';

function diffLineClass(line: string): string {
  if (line.startsWith('+') && !line.startsWith('+++')) return 'diff-add';
  if (line.startsWith('-') && !line.startsWith('---')) return 'diff-del';
  if (line.startsWith('@@')) return 'diff-hunk';
  return 'diff-ctx';
}

function DiffBlock({ diff }: { diff: string }) {
  return (
    <pre className="q-diff">
      {diff.split('\n').map((line, i) => (
        <span key={i} className={diffLineClass(line)}>
          {line || ' '}
        </span>
      ))}
    </pre>
  );
}

export interface QuestionCardProps {
  subject: string;
  open: OpenQuestion;
  onError: (err: Error) => void;
}

export function QuestionCard({ subject, open, onError }: QuestionCardProps) {
  const { question, questionId } = open;
  const hasOptions = question.options.length > 0;
  const [selected, setSelected] = useState<string[]>([]);
  const [other, setOther] = useState('');
  const [notes, setNotes] = useState('');
  const mutation = useAnswer(subject, onError);

  function toggle(option: Option) {
    setSelected((prev) => {
      if (question.multiSelect) {
        return prev.includes(option.label)
          ? prev.filter((l) => l !== option.label)
          : [...prev, option.label];
      }
      return prev.includes(option.label) ? [] : [option.label];
    });
  }

  const filled = selected.length > 0 || other.trim() !== '' || notes.trim() !== '';
  const canSubmit = filled && !mutation.isPending;

  function submit() {
    if (!canSubmit) return;
    mutation.mutate({
      question_id: questionId,
      selected,
      ...(other.trim() !== '' && { other: other.trim() }),
      ...(notes.trim() !== '' && { notes: notes.trim() }),
    });
  }

  return (
    <section className="q-card">
      {question.header && <span className="q-header">{question.header}</span>}
      {question.prompt && <p className="q-prompt">{question.prompt}</p>}
      {question.reasoning && (
        <p className="q-reasoning">
          <span className="q-label">Reasoning</span>
          {question.reasoning}
        </p>
      )}
      {question.diff && <DiffBlock diff={question.diff} />}

      {hasOptions && (
        <div className="q-options" role={question.multiSelect ? 'group' : 'radiogroup'}>
          {question.options.map((option) => {
            const on = selected.includes(option.label);
            return (
              <button
                key={option.label}
                type="button"
                className={`q-option${on ? ' on' : ''}`}
                aria-pressed={on}
                onClick={() => toggle(option)}
              >
                <span className="q-option-label">{option.label}</span>
                {option.description && <span className="q-option-desc">{option.description}</span>}
                {option.preview && <span className="q-option-preview">{option.preview}</span>}
              </button>
            );
          })}
        </div>
      )}

      <label className="q-field">
        <span className="q-field-label">{hasOptions ? 'Other answer' : 'Your answer'}</span>
        <textarea
          className="q-textarea"
          rows={hasOptions ? 1 : 3}
          value={other}
          placeholder={hasOptions ? 'Type an answer instead of picking…' : 'Type your answer…'}
          onChange={(e) => setOther(e.target.value)}
        />
      </label>

      <label className="q-field">
        <span className="q-field-label">Notes</span>
        <textarea
          className="q-textarea"
          rows={1}
          value={notes}
          placeholder="Optional notes…"
          onChange={(e) => setNotes(e.target.value)}
        />
      </label>

      <div className="q-actions">
        <button type="button" className="primary" disabled={!canSubmit} onClick={submit}>
          {mutation.isPending ? 'Sending…' : 'Submit answer'}
        </button>
        {question.multiSelect && hasOptions && <span className="q-hint">Select all that apply</span>}
      </div>
    </section>
  );
}
