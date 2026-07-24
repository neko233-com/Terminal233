import { X } from 'lucide-react'
import type { PropsWithChildren, ReactNode } from 'react'

interface ModalProps extends PropsWithChildren {
  title: string
  description?: string
  icon?: ReactNode
  onClose: () => void
}

export function Modal({ children, title, description, icon, onClose }: ModalProps) {
  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={onClose}>
      <section
        aria-labelledby="modal-title"
        aria-modal="true"
        className="modal-panel"
        role="dialog"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="modal-header">
          <div className="modal-title-wrap">
            {icon ? <span className="modal-title-icon">{icon}</span> : null}
            <div>
              <h2 id="modal-title">{title}</h2>
              {description ? <p>{description}</p> : null}
            </div>
          </div>
          <button aria-label="关闭对话框" className="icon-button subtle" onClick={onClose} type="button">
            <X aria-hidden="true" size={18} />
          </button>
        </header>
        {children}
      </section>
    </div>
  )
}
