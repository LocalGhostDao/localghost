package profile

// Unlock is not instant on a cold account: the container mounts (TPM unseal + dm-crypt), then the
// account's Postgres and Redis start against the decrypted store. ghost.secd streams its progress
// through these stages so the app can show a real loading state.
//
// The stream is IDENTICAL for every unlock. A wipe-PIN entry emits the exact same stages in the same
// order with the same labels as a real one, so the progress itself reveals nothing about which
// account is opening. The only legitimate variation is warmth: an already-mounted (hot) account
// reports the mount/DB stages as Skipped because they are genuinely already done, and a cold account
// streams them for real. Warmth tracks how recently you used the box, not which account is real, so
// it is not a tell , and it is what makes a hot unlock fast (skip, skip, skip) while a cold one
// honestly takes its time.
type Stage int

const (
	StageResolve   Stage = iota // checking the PIN
	StageUnseal                 // TPM unseal of the account key
	StageMount                  // dm-crypt map the container
	StageStartDB                // start this account's Postgres
	StageStartCache             // start this account's Redis
	StageDaemons                // bring the ghost.<x>d daemons online for this account
	StageModel                  // oracled loading the inference model (informational: never blocks done)
	StageReady                  // account is open

	// Teardown stages, mirroring the mount in reverse so a lock reads as a real cold spin-down and the
	// next unlock is visibly fresh (full mount stages, not Skipped).
	StageStopServices // stopping the ghost.<x>d daemons
	StageStopCache    // stop this account's Redis
	StageStopDB       // stop this account's Postgres
	StageUnmount      // luksClose + unmount the container (key leaves the kernel)
	StageLocked       // account is cold again
)

func (s Stage) Label() string {
	switch s {
	case StageResolve:
		return "checking"
	case StageUnseal:
		return "unsealing key"
	case StageMount:
		return "mounting store"
	case StageStartDB:
		return "starting database"
	case StageStartCache:
		return "starting cache"
	case StageDaemons:
		return "starting services"
	case StageModel:
		return "loading model"
	case StageReady:
		return "ready"
	case StageStopServices:
		return "stopping services"
	case StageStopCache:
		return "stopping cache"
	case StageStopDB:
		return "stopping database"
	case StageUnmount:
		return "unmounting store"
	case StageLocked:
		return "locked"
	default:
		return "working"
	}
}

// StepState is how a stage progressed in this unlock.
type StepState int

const (
	Running StepState = iota
	Skipped           // already warm: genuinely nothing to do
	Complete
	Errored
)

// Progress is one streamed update. The app renders Label + State as a loading line. The shape is the
// same on every account; the app cannot (and must not) infer realness from it.
type Progress struct {
	Stage Stage
	State StepState
}

// unlockStages is the fixed ordered stage list every unlock walks. Fixed order + fixed labels are
// what keep the stream uniform across accounts.
var unlockStages = []Stage{
	StageResolve, StageUnseal, StageMount, StageStartDB, StageStartCache, StageDaemons, StageModel, StageReady,
}

// LockStages is the ordered teardown sequence a lock walks, the mount in reverse. Exported so the
// daemon can drive the spin-down and report each step.
var LockStages = []Stage{
	StageStopServices, StageStopCache, StageStopDB, StageUnmount, StageLocked,
}

// StreamUnlock walks the stages and emits Progress for each. `warm` reports, per stage, whether that
// work is already done (an already-mounted account skips unseal/mount/DB/cache); `run` performs the
// stage when it is not warm. Resolve and Ready always run. The emitter (emit) is how ghost.secd
// pushes each update to the client; it returns whichever account this unlock opened to the caller
// unchanged , the stream is presentation, the decision was already made.
//
// Hot path: warm returns true for the heavy stages, so they emit Skipped and the unlock is fast
// (skip, skip, skip, ready). Cold path: warm returns false, each heavy stage Runs and Completes, and
// the app shows a genuine, honest loading sequence , the same sequence whatever account it is.
func StreamUnlock(
	warm func(Stage) bool,
	run func(Stage) error,
	emit func(Progress),
) error {
	for _, st := range unlockStages {
		if st == StageResolve || st == StageReady {
			emit(Progress{Stage: st, State: Running})
			if err := run(st); err != nil {
				emit(Progress{Stage: st, State: Errored})
				return err
			}
			emit(Progress{Stage: st, State: Complete})
			continue
		}
		if warm(st) {
			emit(Progress{Stage: st, State: Skipped})
			continue
		}
		emit(Progress{Stage: st, State: Running})
		if err := run(st); err != nil {
			emit(Progress{Stage: st, State: Errored})
			return err
		}
		emit(Progress{Stage: st, State: Complete})
	}
	return nil
}
